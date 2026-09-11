package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service/metric"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
)

// Task016 Step 5 — Protocol v2 with four separated identity classes (§3.1) and a
// field-diffing comparator (§3.5). This file is the COMPUTATION layer; it is pure
// and secret-free. Protocol v1 remains read-compatible and is never auto-rewritten
// into v2 (I14).
//
// The four identities:
//   quality_protocol_hash       — can two quality results be compared?
//   performance_protocol_hash   — can latency/throughput be compared?
//   run_provenance_hash         — where did this result come from? (never comparison)
//   measurement_contract_hash   — how are metrics computed from facts?
//
// The v2 snapshot stores each sub-document AND its derived hash so a reader can
// always recompute and verify the identity. Secrets and plaintext prompts are
// never present: prompts are digest-only, endpoints are digest-only, API keys are
// absent (I06).

const evaluationProtocolSchemaVersionV2 = "evaluation_protocol/2"

// Comparability is the §3.5 comparability status enum. ERROR is reserved for
// malformed inputs (never fabricated into COMPARABLE).
type Comparability string

const (
	Comparable               Comparability = "COMPARABLE"
	NotComparableDataset     Comparability = "NOT_COMPARABLE_DATASET"
	NotComparablePipeline    Comparability = "NOT_COMPARABLE_PIPELINE"
	NotComparableMetric      Comparability = "NOT_COMPARABLE_METRIC"
	NotComparablePerformance Comparability = "NOT_COMPARABLE_PERFORMANCE_PROFILE"
	NotComparableMeasurement Comparability = "NOT_COMPARABLE_MEASUREMENT"
	ComparabilityError       Comparability = "ERROR"
)

// ComparabilityResult is the machine-readable comparator output. It always lists
// the mismatched field paths; it never returns a bare boolean (§3.5).
type ComparabilityResult struct {
	Status           Comparability `json:"status"`
	MismatchedFields []string      `json:"mismatched_fields"`
}

// ModelComputationFingerprint is the secret-free computation identity of a model
// deployment (KF-06). It carries provider/source, model/deployment revision, a
// digest-only endpoint identity, and a digest of the non-sensitive parameters that
// affect computation. API keys never appear here.
type ModelComputationFingerprint struct {
	Role               string `json:"role"`                // embedding | chat | rerank
	Provider           string `json:"provider"`            // provider/source
	ModelID            string `json:"model_id"`            //
	DeploymentRevision string `json:"deployment_revision"` // deployment/revision (may be empty)
	EndpointDigest     string `json:"endpoint_digest"`     // sha256 of secret-free endpoint identity
	ParamsDigest       string `json:"params_digest"`       // sha256 of non-sensitive computation params
}

// GenerationParamsV2 holds the non-secret generation parameters that affect
// quality (prompt text is digest-only and lives in the v1 snapshot if needed).
type GenerationParamsV2 struct {
	MaxTokens           int     `json:"max_tokens"`
	RepeatPenalty       float64 `json:"repeat_penalty"`
	TopK                int     `json:"top_k"`
	TopP                float64 `json:"top_p"`
	FrequencyPenalty    float64 `json:"frequency_penalty"`
	PresencePenalty     float64 `json:"presence_penalty"`
	Temperature         float64 `json:"temperature"`
	Seed                int     `json:"seed"`
	MaxCompletionTokens int     `json:"max_completion_tokens"`
}

// QualityProtocolV2 is the quality comparison identity. A change to any field here
// must change quality_protocol_hash and surface as a pipeline/dataset/metric
// mismatch (never silently comparable).
type QualityProtocolV2 struct {
	SchemaVersion         string                        `json:"schema_version"`
	DatasetID             string                        `json:"dataset_id"`
	DatasetContentHash    string                        `json:"dataset_content_hash"`
	SourceKnowledgeBaseID string                        `json:"source_knowledge_base_id,omitempty"`
	LineageSchemaVersion  string                        `json:"lineage_schema_version"`
	ParserVersion         string                        `json:"parser_version"`
	ChunkerContractHash   string                        `json:"chunker_contract_hash"`
	IndexBackend          string                        `json:"index_backend"`
	DistanceMetric        string                        `json:"distance_metric"`
	EmbeddingDimension    int                           `json:"embedding_dimension"`
	VectorThreshold       float64                       `json:"vector_threshold"`
	KeywordThreshold      float64                       `json:"keyword_threshold"`
	EmbeddingTopK         int                           `json:"embedding_top_k"`
	RerankTopK            int                           `json:"rerank_top_k"`
	RerankThreshold       float64                       `json:"rerank_threshold"`
	MaxRounds             int                           `json:"max_rounds"`
	FallbackStrategy      string                        `json:"fallback_strategy"`
	EnableRewrite         bool                          `json:"enable_rewrite"`
	EnableQueryExpansion  bool                          `json:"enable_query_expansion"`
	GenerationParams      GenerationParamsV2            `json:"generation_params"`
	TemplateDigests       map[string]string             `json:"template_digests"`
	ModelFingerprints     []ModelComputationFingerprint `json:"model_fingerprints"`
	MetricContractHash    string                        `json:"metric_contract_hash"`
}

// PerformanceProtocolV2 is the performance comparison identity. It folds the
// quality protocol (quality is a prerequisite for performance comparability) plus
// concurrency, timeout/retry, hardware class, warmup and cache state.
type PerformanceProtocolV2 struct {
	SchemaVersion string `json:"schema_version"`
	QualityHash   string `json:"quality_protocol_hash"`
	WorkerCount   int    `json:"worker_count"`
	Concurrency   int    `json:"concurrency"`
	Timeout       string `json:"timeout"` // canonical duration string (e.g. "30s")
	Retry         int    `json:"retry"`
	HardwareClass string `json:"hardware_class"`
	Warmup        string `json:"warmup"`
	CacheState    string `json:"cache_state"`
	LeaseTTL      string `json:"lease_ttl"`
	Heartbeat     string `json:"heartbeat_interval"`
}

// RunProvenanceV2 explains where a result came from. It is provenance, never
// comparison identity; a change here must NOT change the quality hash (I05).
type RunProvenanceV2 struct {
	SchemaVersion      string `json:"schema_version"`
	GitCommit          string `json:"git_commit"`
	AppVersion         string `json:"app_version"`
	GoVersion          string `json:"go_version"`
	BuildTime          string `json:"build_time"`
	DBDriver           string `json:"db_driver"`
	OSArch             string `json:"os_arch"`
	ContainerDigest    string `json:"container_digest"`
	EndpointHostDigest string `json:"endpoint_host_digest"`
	StartedAt          string `json:"started_at"`
}

// ProtocolV2 is the immutable, secret-free v2 protocol snapshot. The four hashes
// are derived from the corresponding sub-documents so a reader can recompute them.
type ProtocolV2 struct {
	SchemaVersion           string                `json:"schema_version"`
	QualityHash             string                `json:"quality_protocol_hash"`
	PerformanceHash         string                `json:"performance_protocol_hash"`
	ProvenanceHash          string                `json:"run_provenance_hash"`
	MeasurementContractHash string                `json:"measurement_contract_hash"`
	Quality                 QualityProtocolV2     `json:"quality"`
	Performance             PerformanceProtocolV2 `json:"performance"`
	Provenance              RunProvenanceV2       `json:"provenance"`
}

// protocolV2Input is the immutable set of facts used to build a v2 snapshot. It is
// decoupled from live config so the builder and comparator are unit-testable.
type protocolV2Input struct {
	DatasetID             string
	DatasetContentHash    string
	SourceKnowledgeBaseID string
	LineageSchemaVersion  string
	ParserVersion         string
	ChunkerContractHash   string
	IndexBackend          string
	DistanceMetric        string
	EmbeddingDimension    int
	VectorThreshold       float64
	KeywordThreshold      float64
	EmbeddingTopK         int
	RerankTopK            int
	RerankThreshold       float64
	MaxRounds             int
	FallbackStrategy      string
	EnableRewrite         bool
	EnableQueryExpansion  bool
	GenerationParams      GenerationParamsV2
	TemplateDigests       map[string]string
	ModelFingerprints     []ModelComputationFingerprint

	WorkerCount   int
	Concurrency   int
	Timeout       string
	Retry         int
	HardwareClass string
	Warmup        string
	CacheState    string
	LeaseTTL      string
	Heartbeat     string

	Provenance RunProvenanceV2
}

// canonicalHashV2 returns the sha256 of the canonical JSON of v. Field order is
// struct-declaration order, which encoding/json preserves, so serialization is
// deterministic across processes (W3).
func canonicalHashV2(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		// Marshal of these fixed structs cannot fail; fail loudly if it ever does.
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// digestV2 returns a secret-free digest of an arbitrary identity string (endpoints,
// params). It is the ONLY form in which endpoint/param identity enters a snapshot.
func digestV2(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// buildProtocolV2 constructs the v2 snapshot and derives all four identity hashes.
// measurement_contract_hash reuses the Step 4 self-hashing metric contract (F05).
func buildProtocolV2(in protocolV2Input) (*ProtocolV2, error) {
	metricContractHash := metric.MeasurementContractHash()

	// Sort model fingerprints by (role, provider, model_id) so model ordering is
	// deterministic and cannot perturb the quality hash.
	fps := append([]ModelComputationFingerprint(nil), in.ModelFingerprints...)
	sort.SliceStable(fps, func(i, j int) bool {
		if fps[i].Role != fps[j].Role {
			return fps[i].Role < fps[j].Role
		}
		if fps[i].Provider != fps[j].Provider {
			return fps[i].Provider < fps[j].Provider
		}
		return fps[i].ModelID < fps[j].ModelID
	})

	quality := QualityProtocolV2{
		SchemaVersion:         evaluationProtocolSchemaVersionV2,
		DatasetID:             in.DatasetID,
		DatasetContentHash:    in.DatasetContentHash,
		SourceKnowledgeBaseID: in.SourceKnowledgeBaseID,
		LineageSchemaVersion:  in.LineageSchemaVersion,
		ParserVersion:         in.ParserVersion,
		ChunkerContractHash:   in.ChunkerContractHash,
		IndexBackend:          in.IndexBackend,
		DistanceMetric:        in.DistanceMetric,
		EmbeddingDimension:    in.EmbeddingDimension,
		VectorThreshold:       in.VectorThreshold,
		KeywordThreshold:      in.KeywordThreshold,
		EmbeddingTopK:         in.EmbeddingTopK,
		RerankTopK:            in.RerankTopK,
		RerankThreshold:       in.RerankThreshold,
		MaxRounds:             in.MaxRounds,
		FallbackStrategy:      in.FallbackStrategy,
		EnableRewrite:         in.EnableRewrite,
		EnableQueryExpansion:  in.EnableQueryExpansion,
		GenerationParams:      in.GenerationParams,
		TemplateDigests:       cloneSortedStringMap(in.TemplateDigests),
		ModelFingerprints:     fps,
		MetricContractHash:    metricContractHash,
	}
	qualityHash := canonicalHashV2(quality)

	performance := PerformanceProtocolV2{
		SchemaVersion: evaluationProtocolSchemaVersionV2,
		QualityHash:   qualityHash,
		WorkerCount:   in.WorkerCount,
		Concurrency:   in.Concurrency,
		Timeout:       in.Timeout,
		Retry:         in.Retry,
		HardwareClass: in.HardwareClass,
		Warmup:        in.Warmup,
		CacheState:    in.CacheState,
		LeaseTTL:      in.LeaseTTL,
		Heartbeat:     in.Heartbeat,
	}
	performanceHash := canonicalHashV2(performance)

	provenance := in.Provenance
	if provenance.SchemaVersion == "" {
		provenance.SchemaVersion = evaluationProtocolSchemaVersionV2
	}
	provenanceHash := canonicalHashV2(provenance)

	return &ProtocolV2{
		SchemaVersion:           evaluationProtocolSchemaVersionV2,
		QualityHash:             qualityHash,
		PerformanceHash:         performanceHash,
		ProvenanceHash:          provenanceHash,
		MeasurementContractHash: metricContractHash,
		Quality:                 quality,
		Performance:             performance,
		Provenance:              provenance,
	}, nil
}

// CompareQualityProtocol compares two v2 snapshots for QUALITY comparability. It
// ignores provenance and performance (a provenance-only change must not invalidate
// a quality comparison, I05). It returns a status and the exact mismatched fields.
func CompareQualityProtocol(a, b *ProtocolV2) ComparabilityResult {
	return compareProtocolV2(a, b, false)
}

// ComparePerformanceProtocol compares two v2 snapshots for PERFORMANCE
// comparability, which additionally requires identical performance profile
// (concurrency, hardware, cache, warmup) on top of quality comparability.
func ComparePerformanceProtocol(a, b *ProtocolV2) ComparabilityResult {
	return compareProtocolV2(a, b, true)
}

func compareProtocolV2(a, b *ProtocolV2, includePerformance bool) ComparabilityResult {
	if a == nil || b == nil || a.SchemaVersion == "" || b.SchemaVersion == "" {
		return ComparabilityResult{Status: ComparabilityError, MismatchedFields: []string{"schema_version"}}
	}

	var mismatches []string

	// 1. Measurement contract identity: how metrics are computed from facts.
	//    - a missing/UNVERSIONED contract on either side => NOT_COMPARABLE_MEASUREMENT
	//      (I14, Step 1 containment): the metric definition cannot be established.
	//    - two valid but different contracts => NOT_COMPARABLE_METRIC (F05).
	var measurementStatus Comparability
	aContract := normalizeContractHashV2(a.MeasurementContractHash)
	bContract := normalizeContractHashV2(b.MeasurementContractHash)
	switch {
	case aContract == "" || bContract == "":
		measurementStatus = NotComparableMeasurement
		mismatches = append(mismatches, "measurement_contract_hash")
	case aContract != bContract:
		measurementStatus = NotComparableMetric
		mismatches = append(mismatches, "measurement_contract_hash")
	}

	// 2. Dataset identity.
	hasDataset := false
	if a.Quality.DatasetContentHash != b.Quality.DatasetContentHash {
		mismatches = append(mismatches, "quality.dataset_content_hash")
		hasDataset = true
	}
	if a.Quality.DatasetID != b.Quality.DatasetID {
		mismatches = append(mismatches, "quality.dataset_id")
		hasDataset = true
	}

	// 3. Pipeline (everything else in the quality identity).
	for _, f := range diffQualityFields(a.Quality, b.Quality) {
		mismatches = append(mismatches, f)
	}

	// 4. Performance profile (only when requested).
	hasPerformance := false
	if includePerformance {
		for _, f := range diffPerformanceFields(a.Performance, b.Performance) {
			mismatches = append(mismatches, f)
			hasPerformance = true
		}
	}

	if len(mismatches) == 0 {
		return ComparabilityResult{Status: Comparable}
	}

	// Highest-priority status. Order matters: measurement, metric, dataset,
	// performance, pipeline.
	switch {
	case measurementStatus == NotComparableMeasurement:
		return ComparabilityResult{Status: NotComparableMeasurement, MismatchedFields: mismatches}
	case measurementStatus == NotComparableMetric:
		return ComparabilityResult{Status: NotComparableMetric, MismatchedFields: mismatches}
	case hasDataset:
		return ComparabilityResult{Status: NotComparableDataset, MismatchedFields: mismatches}
	case hasPerformance:
		return ComparabilityResult{Status: NotComparablePerformance, MismatchedFields: mismatches}
	default:
		return ComparabilityResult{Status: NotComparablePipeline, MismatchedFields: mismatches}
	}
}

// normalizeContractHashV2 treats "" and "UNVERSIONED" as "no valid measurement
// contract" so a missing contract is distinguishable from a changed contract.
func normalizeContractHashV2(s string) string {
	if s == "" || s == "UNVERSIONED" {
		return ""
	}
	return s
}

// diffQualityFields returns the quality field paths (relative to "quality") that
// differ, excluding dataset_id/dataset_content_hash and metric_contract_hash which
// are surfaced with their own statuses.
func diffQualityFields(a, b QualityProtocolV2) []string {
	var out []string
	add := func(name string, differ bool) {
		if differ {
			out = append(out, "quality."+name)
		}
	}
	add("lineage_schema_version", a.LineageSchemaVersion != b.LineageSchemaVersion)
	add("source_knowledge_base_id", a.SourceKnowledgeBaseID != b.SourceKnowledgeBaseID)
	add("parser_version", a.ParserVersion != b.ParserVersion)
	add("chunker_contract_hash", a.ChunkerContractHash != b.ChunkerContractHash)
	add("index_backend", a.IndexBackend != b.IndexBackend)
	add("distance_metric", a.DistanceMetric != b.DistanceMetric)
	add("embedding_dimension", a.EmbeddingDimension != b.EmbeddingDimension)
	add("vector_threshold", a.VectorThreshold != b.VectorThreshold)
	add("keyword_threshold", a.KeywordThreshold != b.KeywordThreshold)
	add("embedding_top_k", a.EmbeddingTopK != b.EmbeddingTopK)
	add("rerank_top_k", a.RerankTopK != b.RerankTopK)
	add("rerank_threshold", a.RerankThreshold != b.RerankThreshold)
	add("max_rounds", a.MaxRounds != b.MaxRounds)
	add("fallback_strategy", a.FallbackStrategy != b.FallbackStrategy)
	add("enable_rewrite", a.EnableRewrite != b.EnableRewrite)
	add("enable_query_expansion", a.EnableQueryExpansion != b.EnableQueryExpansion)
	add("generation_params", a.GenerationParams != b.GenerationParams)
	add("template_digests", !equalStringMaps(a.TemplateDigests, b.TemplateDigests))
	add("model_fingerprints", !equalFingerprints(a.ModelFingerprints, b.ModelFingerprints))
	return out
}

func cloneSortedStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

type modelParamsIdentityV2 struct {
	InterfaceType  string                    `json:"interface_type"`
	Embedding      types.EmbeddingParameters `json:"embedding"`
	ParameterSize  string                    `json:"parameter_size"`
	Provider       string                    `json:"provider"`
	SupportsVision bool                      `json:"supports_vision"`
	MaxConcurrency int                       `json:"max_concurrency"`
}

func endpointIdentityDigestV2(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return digestV2("")
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return digestV2(strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + u.EscapedPath())
}

func runtimeProtocolV2(
	cfg *config.Config, params *types.ChatManage, datasetID, datasetHash, sourceKBID string,
	models []*types.Model, selectedIDs []string, profile EvaluationExecutionProfile, build EvaluationBuildInfo,
) (*ProtocolV2, types.JSON, types.JSON, error) {
	selected := make(map[string]struct{}, len(selectedIDs))
	for _, id := range selectedIDs {
		if id != "" {
			selected[id] = struct{}{}
		}
	}
	fingerprints := make([]ModelComputationFingerprint, 0, len(selected))
	endpointDigests := make([]string, 0, len(selected))
	embeddingDimension := 0
	for _, model := range models {
		if model == nil {
			continue
		}
		if _, ok := selected[model.ID]; !ok {
			continue
		}
		role := strings.ToLower(string(model.Type))
		if model.Type == types.ModelTypeKnowledgeQA {
			role = "chat"
		}
		paramsIdentity := modelParamsIdentityV2{InterfaceType: model.Parameters.InterfaceType, Embedding: model.Parameters.EmbeddingParameters, ParameterSize: model.Parameters.ParameterSize, Provider: model.Parameters.Provider, SupportsVision: model.Parameters.SupportsVision, MaxConcurrency: model.Parameters.MaxConcurrency}
		paramsRaw, err := json.Marshal(paramsIdentity)
		if err != nil {
			return nil, nil, nil, err
		}
		endpointDigest := endpointIdentityDigestV2(model.Parameters.BaseURL)
		endpointDigests = append(endpointDigests, endpointDigest)
		fingerprints = append(fingerprints, ModelComputationFingerprint{Role: role, Provider: string(model.Source), ModelID: model.ID, DeploymentRevision: model.Name, EndpointDigest: endpointDigest, ParamsDigest: digestV2(string(paramsRaw))})
		if model.Type == types.ModelTypeEmbedding {
			embeddingDimension = model.Parameters.EmbeddingParameters.Dimension
		}
	}
	sort.Strings(endpointDigests)
	p := &params.PipelineRequest
	templateDigests := map[string]string{
		"summary": digestV2(p.SummaryConfig.Prompt), "context_template": digestV2(p.SummaryConfig.ContextTemplate),
		"no_match_prefix": digestV2(p.SummaryConfig.NoMatchPrefix), "fallback_response": digestV2(p.FallbackResponse),
		"fallback_template": digestV2(p.FallbackPrompt), "rewrite_system": digestV2(p.RewritePromptSystem),
		"rewrite_user": digestV2(p.RewritePromptUser),
	}
	chunkerRaw, _ := json.Marshal(struct {
		Size, Overlap int
		Markers       []string
	}{})
	indexBackend := "unknown"
	cacheState := "disabled"
	timeout := "unknown"
	if cfg != nil {
		if cfg.KnowledgeBase != nil {
			chunkerRaw, _ = json.Marshal(struct {
				Size, Overlap int
				Markers       []string
			}{cfg.KnowledgeBase.ChunkSize, cfg.KnowledgeBase.ChunkOverlap, cfg.KnowledgeBase.SplitMarkers})
		}
		if cfg.VectorDatabase != nil && cfg.VectorDatabase.Driver != "" {
			indexBackend = cfg.VectorDatabase.Driver
		}
		if cfg.EmbeddingCache != nil && cfg.EmbeddingCache.Enabled {
			cacheState = "configured"
		}
		if cfg.Agent != nil && cfg.Agent.LLMCallTimeout > 0 {
			timeout = (time.Duration(cfg.Agent.LLMCallTimeout) * time.Second).String()
		}
	}
	proto, err := buildProtocolV2(protocolV2Input{
		DatasetID: datasetID, DatasetContentHash: datasetHash, SourceKnowledgeBaseID: sourceKBID,
		LineageSchemaVersion: "stable-lineage/1", ParserVersion: "passage-direct/1", ChunkerContractHash: digestV2(string(chunkerRaw)),
		IndexBackend: indexBackend, DistanceMetric: "configured-default", EmbeddingDimension: embeddingDimension,
		VectorThreshold: p.VectorThreshold, KeywordThreshold: p.KeywordThreshold, EmbeddingTopK: p.EmbeddingTopK,
		RerankTopK: p.RerankTopK, RerankThreshold: p.RerankThreshold, MaxRounds: p.MaxRounds,
		FallbackStrategy: string(p.FallbackStrategy), EnableRewrite: p.EnableRewrite, EnableQueryExpansion: p.EnableQueryExpansion,
		GenerationParams: GenerationParamsV2{MaxTokens: p.SummaryConfig.MaxTokens, RepeatPenalty: p.SummaryConfig.RepeatPenalty, TopK: p.SummaryConfig.TopK, TopP: p.SummaryConfig.TopP, FrequencyPenalty: p.SummaryConfig.FrequencyPenalty, PresencePenalty: p.SummaryConfig.PresencePenalty, Temperature: p.SummaryConfig.Temperature, Seed: p.SummaryConfig.Seed, MaxCompletionTokens: p.SummaryConfig.MaxCompletionTokens},
		TemplateDigests:  templateDigests, ModelFingerprints: fingerprints,
		WorkerCount: profile.Workers, Concurrency: profile.Workers, Timeout: timeout, Retry: 0,
		HardwareClass: runtime.GOOS + "/" + runtime.GOARCH, Warmup: "none", CacheState: cacheState,
		LeaseTTL: profile.LeaseTTL.String(), Heartbeat: profile.HeartbeatInterval.String(),
		Provenance: RunProvenanceV2{GitCommit: build.GitCommit, AppVersion: build.AppVersion, GoVersion: runtime.Version(), BuildTime: build.BuildTime, DBDriver: indexBackend, OSArch: runtime.GOOS + "/" + runtime.GOARCH, EndpointHostDigest: digestV2(strings.Join(endpointDigests, ",")), StartedAt: time.Now().UTC().Format(time.RFC3339Nano)},
	})
	if err != nil {
		return nil, nil, nil, err
	}
	snapshot, err := json.Marshal(proto)
	if err != nil {
		return nil, nil, nil, err
	}
	provenance, err := json.Marshal(proto.Provenance)
	if err != nil {
		return nil, nil, nil, err
	}
	return proto, types.JSON(snapshot), types.JSON(provenance), nil
}

func diffPerformanceFields(a, b PerformanceProtocolV2) []string {
	var out []string
	add := func(name string, differ bool) {
		if differ {
			out = append(out, "performance."+name)
		}
	}
	add("worker_count", a.WorkerCount != b.WorkerCount)
	add("concurrency", a.Concurrency != b.Concurrency)
	add("timeout", a.Timeout != b.Timeout)
	add("retry", a.Retry != b.Retry)
	add("hardware_class", a.HardwareClass != b.HardwareClass)
	add("warmup", a.Warmup != b.Warmup)
	add("cache_state", a.CacheState != b.CacheState)
	add("lease_ttl", a.LeaseTTL != b.LeaseTTL)
	add("heartbeat_interval", a.Heartbeat != b.Heartbeat)
	return out
}

func equalFingerprints(a, b []ModelComputationFingerprint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
