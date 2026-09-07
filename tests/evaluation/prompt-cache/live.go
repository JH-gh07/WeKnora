package main

// Task013 live runner (plan §9 Step 6/7). Raw-HTTP boundary so the raw cache
// fields (prompt_cache_hit/miss_tokens, prompt_tokens_details.cached_tokens)
// are observed exactly as the provider reports them. Credential comes only
// from environment (never .env, never printed). Sequential, concurrency 1,
// atomic pre-call budget check, no retries (attempt observability FULL).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/agent"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/Tencent/WeKnora/tests/evaluation/prompt-cache/fixtures"
)

const (
	liveBaseURL     = "https://api.siliconflow.cn/v1"
	liveModel       = "Qwen/Qwen3-14B"
	liveTemperature = 0.3
	liveMaxTokens   = 32768
	liveInputPrice  = 500  // nano-CNY per token (0.5 CNY / 1M)
	liveOutputPrice = 2000 // nano-CNY per token (2 CNY / 1M)
	interCallDelay  = 2000 * time.Millisecond
	interBlockDelay = 5000 * time.Millisecond
)

type liveConfig struct {
	apiKey       string
	evidenceDir  string
	budgetNanos  int64
	protocolHash string
	experimentID string
}

type providerUsage struct {
	prompt, completion, total int
	cached, hit, miss         *int
	model                     string
}

func resolveCredential() (string, error) {
	if v := os.Getenv("SF_API_KEY"); v != "" {
		return v, nil
	}
	cipher := os.Getenv("SF_API_KEY_CIPHERTEXT")
	aesKey := os.Getenv("SF_AES_KEY")
	if cipher == "" || aesKey == "" {
		return "", fmt.Errorf("credential missing: set SF_API_KEY, or SF_API_KEY_CIPHERTEXT+SF_AES_KEY")
	}
	plain, err := utils.DecryptAESGCM(cipher, []byte(aesKey))
	if err != nil {
		return "", fmt.Errorf("credential decrypt failed: %w", err)
	}
	if plain == "" {
		return "", fmt.Errorf("credential decrypt produced empty key")
	}
	return plain, nil
}

func verifyPrereg(dir string) (map[string]any, error) {
	yamlPath := filepath.Join(dir, "experiment_preregistration.yaml")
	shaPath := filepath.Join(dir, "experiment_preregistration.sha256")
	yamlBytes, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, err
	}
	shaBytes, err := os.ReadFile(shaPath)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(yamlBytes)
	if strings.Fields(string(shaBytes))[0] != hex.EncodeToString(sum[:]) {
		return nil, fmt.Errorf("preregistration sha256 mismatch")
	}
	protoBytes, err := os.ReadFile(filepath.Join(dir, "experiment_preregistration_protocol.json"))
	if err != nil {
		return nil, err
	}
	var proto map[string]any
	if err := json.Unmarshal(protoBytes, &proto); err != nil {
		return nil, fmt.Errorf("protocol JSON: %w", err)
	}
	expect := func(protoKey, file string) error {
		want, _ := proto[protoKey].(string)
		got, err := sha256File(filepath.Join(dir, file))
		if err != nil {
			return err
		}
		if want != got {
			return fmt.Errorf("%s mismatch: prereg %s != file %s", file, want, got)
		}
		return nil
	}
	for _, kv := range [][2]string{
		{"fixture_manifest_hash", "fixture_manifest.json"},
		{"semantic_section_contract_hash", "semantic_section_contract.json"},
		{"quality_contract_hash", "quality_contract.yaml"},
	} {
		if err := expect(kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	builderHash, err := builderArtifactHash()
	if err != nil {
		return nil, err
	}
	if want, _ := proto["production_builder_artifact_hash"].(string); want != builderHash {
		return nil, fmt.Errorf("production_builder_artifact_hash mismatch: prereg %s != files %s", want, builderHash)
	}
	// evaluator_manifest.json carries the hash of validator.go; cross-check it.
	var evManifest struct {
		EvaluatorArtifactHash string `json:"evaluator_artifact_hash"`
		EvaluatorFile         string `json:"evaluator_file"`
	}
	evBytes, _ := os.ReadFile(filepath.Join(dir, "evaluator_manifest.json"))
	if err := json.Unmarshal(evBytes, &evManifest); err != nil {
		return nil, err
	}
	if want, _ := proto["evaluator_artifact_hash"].(string); want != evManifest.EvaluatorArtifactHash {
		return nil, fmt.Errorf("evaluator artifact hash mismatch between prereg and manifest")
	}
	// The evaluator hash must also match the actual validator source file.
	valSum, err := sha256File(filepath.Join("tests", "evaluation", "prompt-cache", "validator.go"))
	if err != nil {
		return nil, fmt.Errorf("validator.go unreadable: %w", err)
	}
	if evManifest.EvaluatorArtifactHash != valSum {
		return nil, fmt.Errorf("evaluator artifact hash mismatch: manifest %s != validator.go %s", evManifest.EvaluatorArtifactHash, valSum)
	}
	return proto, nil
}

func sha256File(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func liveRequestJSON(msgs []chat.Message) []byte {
	req := map[string]any{
		"model":       liveModel,
		"messages":    msgs,
		"stream":      false,
		"temperature": liveTemperature,
		"max_tokens":  liveMaxTokens,
	}
	b, _ := json.Marshal(req)
	return b
}

// callOnce performs one raw-HTTP provider call. No retry (attempt observability
// FULL, attempt_count=1). Response body stays in memory; only allowlisted
// fields leave this function.
func callOnce(apiKey string, msgs []chat.Message) (body []byte, status int, elapsedMS int, traceHash string, err error) {
	reqBody := liveRequestJSON(msgs)
	req, err := http.NewRequest("POST", liveBaseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return nil, 0, 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	client := &http.Client{Timeout: 180 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	elapsedMS = int(time.Since(start).Milliseconds())
	if err != nil {
		return nil, 0, elapsedMS, "", err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, elapsedMS, "", err
	}
	if t := resp.Header.Get("x-siliconcloud-trace-id"); t != "" {
		sum := sha256.Sum256([]byte(t))
		traceHash = hex.EncodeToString(sum[:])
	}
	return body, resp.StatusCode, elapsedMS, traceHash, nil
}

func parseProviderUsage(body []byte) (providerUsage, error) {
	var raw struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			Details          *struct {
				CachedTokens *int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			Hit  *int `json:"prompt_cache_hit_tokens"`
			Miss *int `json:"prompt_cache_miss_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return providerUsage{}, fmt.Errorf("decode response: %w", err)
	}
	u := providerUsage{
		prompt: raw.Usage.PromptTokens, completion: raw.Usage.CompletionTokens,
		total: raw.Usage.TotalTokens, hit: raw.Usage.Hit, miss: raw.Usage.Miss,
		model: raw.Model,
	}
	if raw.Usage.Details != nil {
		u.cached = raw.Usage.Details.CachedTokens
	}
	return u, nil
}

// cacheMapping applies plan §3.3 status semantics at the raw boundary.
func cacheMapping(u providerUsage) (status string, read, miss int) {
	reported := u.hit != nil || u.miss != nil || u.cached != nil
	if !reported {
		return "unreported", 0, 0
	}
	if u.hit != nil {
		read = *u.hit
	} else if u.cached != nil {
		read = *u.cached
	}
	if u.miss != nil {
		miss = *u.miss
		if u.hit != nil && *u.hit+*u.miss != u.prompt {
			return "unknown", read, miss // contract anomaly, fail closed
		}
	} else {
		miss = u.prompt - read
	}
	if read > 0 {
		return "hit", read, miss
	}
	return "miss", read, miss
}

func qualityForOutput(output string, f fixtures.Fixture) Quality {
	return Validate(output, f.RequiredFacts, f.ForbiddenFacts, allowedSlugsFor(f.AvailableSlugs), f.PageSlug)
}

type runState struct {
	spentNanos int64
	calls      int
	rows       []sanitizedRow
	failures   []string
}

func (s *runState) preCallCheck(budget int64) error {
	if s.spentNanos >= budget {
		return fmt.Errorf("BLOCKED_BUDGET: spent %d >= ceiling %d nanos", s.spentNanos, budget)
	}
	if float64(s.spentNanos)/float64(budget) >= 0.80 {
		return fmt.Errorf("BLOCKED_BUDGET: spent >= 80%% of ceiling before next call")
	}
	return nil
}

func (s *runState) record(cfg liveConfig, f fixtures.Fixture, arm string, ordinal int, warmup bool, msgs []chat.Message) error {
	body, status, elapsedMS, traceHash, err := callOnce(cfg.apiKey, msgs)
	attempts := 1
	row := sanitizedRow{
		ExperimentID: cfg.experimentID, ProtocolHash: cfg.protocolHash,
		SampleID: f.ID, Arm: arm, Ordinal: ordinal, Warmup: warmup,
		LogicalCallID: fmt.Sprintf("%s-%s-%d", f.ID, arm, ordinal),
		TraceIDHash:   traceHash, Provider: "siliconflow", ModelExact: liveModel,
		AttemptCount: &attempts, AttemptObservability: "FULL",
		RequestElapsedMS: elapsedMS, PricingRuleID: "task012-frozen-2026-09-06",
		Currency: "CNY",
	}
	if err != nil {
		row.UsageFinality = "UNAVAILABLE"
		row.ErrorType = "provider_error"
		row.IncludedInPrimary = false
		row.ExclusionReason = fmt.Sprintf("provider_error_status_%d", status)
		s.rows = append(s.rows, row)
		s.failures = append(s.failures, fmt.Sprintf("%s/%s: %v", f.ID, arm, err))
		return nil
	}
	if status != 200 {
		row.UsageFinality = "UNAVAILABLE"
		row.ErrorType = fmt.Sprintf("http_%d", status)
		row.IncludedInPrimary = false
		row.ExclusionReason = fmt.Sprintf("http_status_%d", status)
		s.rows = append(s.rows, row)
		s.failures = append(s.failures, fmt.Sprintf("%s/%s: http %d", f.ID, arm, status))
		return nil
	}
	u, perr := parseProviderUsage(body)
	if perr != nil {
		row.UsageFinality = "UNAVAILABLE"
		row.ErrorType = "malformed_response"
		row.IncludedInPrimary = false
		row.ExclusionReason = "provider_response_malformed"
		s.rows = append(s.rows, row)
		s.failures = append(s.failures, fmt.Sprintf("%s/%s: %v", f.ID, arm, perr))
		return nil
	}
	row.ReportedModelRevision = u.model
	row.InputTokens = u.prompt
	row.OutputTokens = u.completion
	row.CacheReportedInput = u.prompt
	statusCache, read, miss := cacheMapping(u)
	row.CacheStatus = statusCache
	row.CacheReadTokens = read
	row.CacheMissTokens = miss
	row.UsageFinality = "REPORTED"
	row.IncludedInPrimary = !warmup
	cost := int64(u.prompt)*liveInputPrice + int64(u.completion)*liveOutputPrice
	row.EstimatedCostNanos = &cost
	s.spentNanos += cost

	var content struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &content); err == nil && len(content.Choices) > 0 {
		q := qualityForOutput(content.Choices[0].Message.Content, f)
		row.SchemaFormatPass = q.SchemaFormatPass
		row.SummaryLinePass = q.SummaryLinePass
		row.ForbiddenHandles = q.ForbiddenInternalHandles
		row.RequiredFactRecall = q.RequiredFactRecall
		row.UnsupportedFactCount = q.UnsupportedFactCount
		row.ValidLinkPass = q.ValidLinkPass
		row.SourceGroundingPass = q.SourceGroundingPass
	} else {
		row.ErrorType = "output_unparseable"
		row.IncludedInPrimary = false
		row.ExclusionReason = "provider_response_malformed"
	}
	s.rows = append(s.rows, row)
	s.calls++
	return nil
}

func emitTSV(path string, rows []sanitizedRow) error {
	var b strings.Builder
	b.WriteString("experiment_id\tprotocol_hash\tsample_id\tarm\tordinal\twarmup\tlogical_call_id\ttrace_id_hash\tprovider\tmodel_exact\treported_model_revision\tinput_tokens\toutput_tokens\tcache_read_tokens\tcache_write_tokens\tcache_miss_tokens\tcache_reported_input_tokens\tcache_status\tusage_finality\tattempt_count\tattempt_observability\trequest_elapsed_ms\tprovider_latency_ms_nullable\tpricing_rule_id\testimated_cost_nanos_nullable\tcurrency_nullable\tschema_format_pass\tsummary_line_pass\tforbidden_internal_handle_count\trequired_fact_recall\tunsupported_fact_count\tvalid_link_pass\tsource_grounding_pass\terror_type\tincluded_in_primary\texclusion_reason\n")
	att := func(p *int) string {
		if p == nil {
			return ""
		}
		return strconv.Itoa(*p)
	}
	cost := func(p *int64) string {
		if p == nil {
			return ""
		}
		return strconv.FormatInt(*p, 10)
	}
	lat := func(p *int) string {
		if p == nil {
			return ""
		}
		return strconv.Itoa(*p)
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%d\t%t\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%t\t%t\t%d\t%.4f\t%d\t%t\t%t\t%s\t%t\t%s\n",
			r.ExperimentID, r.ProtocolHash, r.SampleID, r.Arm, r.Ordinal, r.Warmup,
			r.LogicalCallID, r.TraceIDHash, r.Provider, r.ModelExact, r.ReportedModelRevision,
			r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens, r.CacheMissTokens,
			r.CacheReportedInput, r.CacheStatus, r.UsageFinality, att(r.AttemptCount), r.AttemptObservability,
			r.RequestElapsedMS, lat(r.ProviderLatencyMS), r.PricingRuleID, cost(r.EstimatedCostNanos), r.Currency,
			r.SchemaFormatPass, r.SummaryLinePass, r.ForbiddenHandles, r.RequiredFactRecall,
			r.UnsupportedFactCount, r.ValidLinkPass, r.SourceGroundingPass, r.ErrorType,
			r.IncludedInPrimary, r.ExclusionReason)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// provenance captures the frozen run identity (plan §6.2).
func provenance() map[string]any {
	git := func(args ...string) string {
		out, err := exec.Command("git", args...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return map[string]any{
		"operator":     "task013-agent",
		"actual_start": time.Now().UTC().Format(time.RFC3339),
		"git_commit":   git("rev-parse", "HEAD"),
		"git_tree":     git("rev-parse", "HEAD^{tree}"),
		"environment":  map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH},
	}
}

// loadProtocolHash reads the frozen protocol_hash file and cross-checks it
// against the canonical protocol JSON bytes (trailing newline ignored).
func loadProtocolHash(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "experiment_preregistration.protocol_hash"))
	if err != nil {
		return "", fmt.Errorf("protocol_hash file missing: %w", err)
	}
	frozen := strings.TrimSpace(string(b))
	jsonBytes, err := os.ReadFile(filepath.Join(dir, "experiment_preregistration_protocol.json"))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.TrimRight(string(jsonBytes), "\n")))
	if hex.EncodeToString(sum[:]) != frozen {
		return "", fmt.Errorf("protocol_hash mismatch: frozen %s != computed %s", frozen, hex.EncodeToString(sum[:]))
	}
	return frozen, nil
}

// treatmentMessages builds the treatment arm through the production builder.
func treatmentMessages(f fixtures.Fixture) ([]chat.Message, error) {
	return agent.BuildWikiPageModifyMessages(f.DataMap())
}

// controlMessagesFor builds the control arm from the same canonical sections.
func controlMessagesFor(f fixtures.Fixture) ([]chat.Message, error) {
	msgs, err := agent.BuildWikiPageModifyMessages(f.DataMap())
	if err != nil {
		return nil, err
	}
	sections, err := treatmentSections(msgs[0].Content, msgs[1].Content)
	if err != nil {
		return nil, err
	}
	ctrlSections, err := controlSections(sections)
	if err != nil {
		return nil, err
	}
	return controlMessages(ctrlSections)
}

func runPilot(cfg liveConfig) error {
	fmt.Println("pilot: verifying preregistration...")
	if _, err := verifyPrereg(cfg.evidenceDir); err != nil {
		return fmt.Errorf("preregistration verification failed: %w", err)
	}
	probeYAML, err := os.ReadFile(filepath.Join(cfg.evidenceDir, "capability_probe_preregistration.yaml"))
	if err != nil {
		return fmt.Errorf("probe preregistration missing: %w", err)
	}
	probeSHA, err := os.ReadFile(filepath.Join(cfg.evidenceDir, "capability_probe_preregistration.sha256"))
	if err != nil {
		return fmt.Errorf("probe preregistration sha missing: %w", err)
	}
	probeSum := sha256.Sum256(probeYAML)
	if strings.Fields(string(probeSHA))[0] != hex.EncodeToString(probeSum[:]) {
		return fmt.Errorf("probe preregistration sha256 mismatch")
	}
	pilot := fixtures.PilotPair()
	state := &runState{}
	// Per capability_probe_preregistration: call1 P1, call2 identical P1,
	// call3 P2; call4 (optional stream variant) skipped by design (non_stream_primary).
	plan := []struct {
		f   fixtures.Fixture
		arm string
	}{
		{pilot[0], "pilot"},
		{pilot[0], "pilot"},
		{pilot[1], "pilot"},
	}
	for i, step := range plan {
		if err := state.preCallCheck(cfg.budgetNanos); err != nil {
			return err
		}
		msgs, err := treatmentMessages(step.f)
		if err != nil {
			return err
		}
		if err := state.record(cfg, step.f, step.arm, i+1, false, msgs); err != nil {
			return err
		}
		fmt.Printf("pilot call %d/%d done (status=%s spent=%d nanos)\n", i+1, len(plan), state.rows[len(state.rows)-1].CacheStatus, state.spentNanos)
		if i+1 < len(plan) {
			time.Sleep(interCallDelay)
		}
	}
	for _, r := range state.rows {
		if r.CacheStatus == "hit" {
			fmt.Println("pilot outcome: HITS_OBSERVED")
			goto done
		}
	}
	fmt.Println("pilot outcome: NO_HITS_OBSERVED (main A/B prefix target not frozen -> §5.3 INCONCLUSIVE_TELEMETRY)")
done:
	if err := emitTSV(filepath.Join(cfg.evidenceDir, "pilot_sanitized.tsv"), state.rows); err != nil {
		return err
	}
	summary := map[string]any{
		"pilot_id": "task013-pilot-v1", "planned_calls": 3, "actual_calls": state.calls,
		"spent_nanos": state.spentNanos, "failures": state.failures,
		"call4_stream_variant": "skipped_by_design (non_stream_primary)",
		"provenance":           provenance(),
	}
	if err := writeJSON(filepath.Join(cfg.evidenceDir, "pilot_run_summary.json"), summary); err != nil {
		return err
	}
	fmt.Printf("pilot complete: %d calls, spent %d nanos (%.6f CNY)\n", state.calls, state.spentNanos, float64(state.spentNanos)/1e9)
	return nil
}

func runMain(cfg liveConfig) error {
	fmt.Println("main: verifying preregistration...")
	if _, err := verifyPrereg(cfg.evidenceDir); err != nil {
		return fmt.Errorf("preregistration verification failed: %w", err)
	}
	all := fixtures.All()
	byID := map[string]fixtures.Fixture{}
	for _, f := range all {
		byID[f.ID] = f
	}
	state := &runState{}
	ord := 0
	call := func(f fixtures.Fixture, arm string, warmup bool) error {
		if err := state.preCallCheck(cfg.budgetNanos); err != nil {
			return err
		}
		ord++
		var msgs []chat.Message
		var err error
		if arm == "control" {
			msgs, err = controlMessagesFor(f)
		} else {
			msgs, err = treatmentMessages(f)
		}
		if err != nil {
			return err
		}
		if err := state.record(cfg, f, arm, ord, warmup, msgs); err != nil {
			return err
		}
		last := state.rows[len(state.rows)-1]
		fmt.Printf("call %02d %s/%s status=%s finality=%s spent=%d nanos\n", ord, f.ID, arm, last.CacheStatus, last.UsageFinality, state.spentNanos)
		return nil
	}

	// Warmup: one call per arm, excluded from primary aggregation.
	warmF := byID["t013-cache-abi/instance-01"]
	if err := call(warmF, "treatment", true); err != nil {
		return err
	}
	time.Sleep(interCallDelay)
	if err := call(warmF, "control", true); err != nil {
		return err
	}
	time.Sleep(interBlockDelay)

	// Order schedule (plan §7.2): 3 blocks × 4 pairs, fixed alternation.
	type pair struct {
		aFirst bool
	}
	orders := []pair{
		{true}, {false}, {true}, {false},
		{false}, {true}, {false}, {true},
		{true}, {false}, {true}, {false},
	}
	for i, f := range all {
		armFirst, armSecond := "treatment", "control"
		if !orders[i].aFirst {
			armFirst, armSecond = "control", "treatment"
		}
		if err := call(f, armFirst, false); err != nil {
			return err
		}
		time.Sleep(interCallDelay)
		if err := call(f, armSecond, false); err != nil {
			return err
		}
		if i+1 < len(all) {
			if (i+1)%4 == 0 {
				time.Sleep(interBlockDelay)
			} else {
				time.Sleep(interCallDelay)
			}
		}
	}

	if err := emitTSV(filepath.Join(cfg.evidenceDir, "authoritative_sanitized.tsv"), state.rows); err != nil {
		return err
	}
	summary := map[string]any{
		"experiment_id": cfg.experimentID, "protocol_hash": cfg.protocolHash,
		"planned_logical_calls": 26, "actual_logical_calls": state.calls,
		"provider_bound_calls": state.calls, "coalescing": "NONE",
		"attempt_observability": "FULL", "retries": "none (single attempt per call)",
		"spent_nanos": state.spentNanos, "failures": state.failures,
		"provenance": provenance(),
	}
	if err := writeJSON(filepath.Join(cfg.evidenceDir, "authoritative_run_summary.json"), summary); err != nil {
		return err
	}
	fmt.Printf("main complete: %d calls, spent %d nanos (%.6f CNY)\n", state.calls, state.spentNanos, float64(state.spentNanos)/1e9)
	return nil
}
