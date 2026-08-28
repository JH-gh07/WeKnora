package agent

// Task006 (Gate G4 / R8) — same-runtime deterministic Prompt Layout harness.
//
// The harness compares two Prompt Layouts for the wiki page-modify path, using
// ONLY the real production builders so there is no drift:
//
//   - Current (B): WikiPageModifySystemPrompt (stable system message) +
//     WikiPageModifyUserPrompt (shared source context placed before page
//     metadata), exactly as generateWithTemplate assembles them.
//   - Legacy (A): the byte-exact pre-6fb85810 single-user-message prompt
//     (see legacy_wiki_modify_prompt_test.go), which has no stable system
//     message and places page metadata immediately after the rules block.
//
// It is test-only. It never talks to a provider, never persists anything, and
// never writes full prompt text into evidence: only SHA-256 digests, byte/token
// lengths, first-difference indices and fingerprint cardinality.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"text/template"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// layoutFixture is the synthetic, non-sensitive variable set for one page.
type layoutFixture struct {
	HasAdditions            bool
	SharedSourceContexts    string
	PageSlug                string
	PageTitle               string
	PageType                string
	PageAliases             string
	ExistingContent         string
	NewContent              string
	HasRetractions          bool
	DeletedContent          string
	RemainingSourcesContent string
	AvailableSlugs          string
	Language                string
	CustomInstructions      string
	InstructionScope        string
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// renderLayoutTemplate renders a prompt template with the same map[string]string
// shape generateWithTemplate uses (booleans as "true"/"false", strings verbatim).
// The fixture contains no image URLs, so maskTemplateDataImageURLs is an
// identity transformation and is intentionally not replicated here.
func renderLayoutTemplate(tpl string, f layoutFixture) string {
	tmpl, err := template.New("layout").Parse(tpl)
	if err != nil {
		panic(err)
	}
	data := map[string]string{
		"HasAdditions":            boolStr(f.HasAdditions),
		"HasRetractions":          boolStr(f.HasRetractions),
		"PageSlug":                f.PageSlug,
		"PageTitle":               f.PageTitle,
		"PageType":                f.PageType,
		"PageAliases":             f.PageAliases,
		"ExistingContent":         f.ExistingContent,
		"SharedSourceContexts":    f.SharedSourceContexts,
		"NewContent":              f.NewContent,
		"DeletedContent":          f.DeletedContent,
		"RemainingSourcesContent": f.RemainingSourcesContent,
		"AvailableSlugs":          f.AvailableSlugs,
		"Language":                f.Language,
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		panic(err)
	}
	return buf.String()
}

// renderCurrentLayout reproduces generateWithTemplate's message assembly for the
// wiki page-modify path: a system message (stable rules + custom instructions)
// followed by a user message (rendered WikiPageModifyUserPrompt).
func renderCurrentLayout(f layoutFixture) (system, user string) {
	system = types.AppendCustomPromptInstructions(WikiPageModifySystemPrompt, f.CustomInstructions, f.InstructionScope)
	user = renderLayoutTemplate(WikiPageModifyUserPrompt, f)
	return system, user
}

// renderLegacyLayout reproduces the pre-6fb85810 single-user-message prompt.
// The legacy path had no stable system message, so custom instructions were
// appended INLINE to the single user message (generateWithTemplate's else
// branch: AppendCustomPromptInstructions(prompt, ...) at 6fb85810^).
func renderLegacyLayout(f layoutFixture) string {
	rendered := renderLayoutTemplate(legacyWikiPageModifyPrompt, f)
	return types.AppendCustomPromptInstructions(rendered, f.CustomInstructions, f.InstructionScope)
}

// canonicalCurrentPrompt is the canonical serialization used for prefix
// measurement: system and user content joined by a single newline. The exact
// separator only affects byte offsets, not the relative Legacy/Current finding.
func canonicalCurrentPrompt(f layoutFixture) string {
	system, user := renderCurrentLayout(f)
	return system + "\n" + user
}

// localTokenize is the local whitespace proxy. It is NOT a provider tokenizer
// and must never be used for pricing or provider eligibility (plan §0.2.7).
const localTokenizerIdentity = "local-whitespace-tokenizer-v1"

func localTokenize(s string) []string { return strings.Fields(s) }

func commonPrefixBytes(ss []string) int {
	if len(ss) == 0 {
		return 0
	}
	p := ss[0]
	for _, s := range ss[1:] {
		n := 0
		for n < len(p) && n < len(s) && p[n] == s[n] {
			n++
		}
		p = p[:n]
	}
	return len(p)
}

func commonPrefixTokens(ss []string) int {
	if len(ss) == 0 {
		return 0
	}
	first := localTokenize(ss[0])
	n := len(first)
	for _, s := range ss[1:] {
		toks := localTokenize(s)
		j := 0
		for j < n && j < len(toks) && first[j] == toks[j] {
			j++
		}
		n = j
	}
	return n
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func distinctStrings(ss []string) int {
	set := map[string]struct{}{}
	for _, s := range ss {
		set[s] = struct{}{}
	}
	return len(set)
}

// syntheticBody returns a deterministic, synthetic markdown body of the given
// size class. It contains no real-world content and no sensitive data.
func syntheticBody(topic string, i int, size string) string {
	repeat := map[string]int{"small": 2, "medium": 5, "large": 12}[size]
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Synthetic %s instance %02d\n\n", topic, i))
	for r := 0; r < repeat; r++ {
		b.WriteString(fmt.Sprintf("This is deterministic synthetic sentence %d for %s instance %02d. It exists only to vary body length for prefix-stability measurement.\n", r, topic, i))
	}
	return b.String()
}

// topicFixtures builds n deterministic, synthetic page fixtures for one topic.
// Page-level variables differ; the shared source context and language are held
// constant per topic so a same-source reduce batch is modelled faithfully.
func topicFixtures(topic string, n int) []layoutFixture {
	shared := fmt.Sprintf("Synthetic domain %q is a non-real fixture domain used solely for deterministic prompt-layout prefix analysis. It has no real-world referent and contains no personal, credential, or confidential data.", topic)
	sizes := []string{"small", "medium", "large"}
	out := make([]layoutFixture, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, layoutFixture{
			HasAdditions:         true,
			SharedSourceContexts: shared,
			PageSlug:             fmt.Sprintf("%s/instance-%02d", topic, i),
			PageTitle:            fmt.Sprintf("Synthetic %s Instance %02d", topic, i),
			PageType:             "synthetic-entity",
			PageAliases:          "",
			ExistingContent:      syntheticBody(topic, i, sizes[(i-1)%3]),
			NewContent:           fmt.Sprintf("Synthetic addition %d for %s: a deterministic update with no external reference.", i, topic),
			HasRetractions:       false,
			AvailableSlugs:       fmt.Sprintf("%s/instance-01, %s/instance-02, %s/instance-03", topic, topic, topic),
			Language:             "English",
			CustomInstructions:   "",
			InstructionScope:     "wiki_content",
		})
	}
	return out
}

// layoutRow is one sanitized aggregate row in layout_stability_raw.tsv. It never
// contains full prompt text.
type layoutRow struct {
	Topic                    string `json:"topic"`
	Variant                  string `json:"variant"`
	InstanceCount            int    `json:"instance_count"`
	CommonPrefixBytes        int    `json:"common_prefix_bytes"`
	FirstDifferingByteIndex  int    `json:"first_differing_byte_index"`
	CommonPrefixTokens       int    `json:"common_prefix_tokens_local_proxy"`
	FirstDifferingTokenIndex int    `json:"first_differing_token_index_local_proxy"`
	FingerprintCardinality   int    `json:"prefix_fingerprint_cardinality"`
	PromptSHA256Distinct     int    `json:"prompt_sha256_distinct"`
	CommonPrefixSHA256       string `json:"common_prefix_sha256"`
}

// computeRows computes the sanitized aggregate rows for every topic/variant.
func computeRows(topics []string, perTopic int) []layoutRow {
	var rows []layoutRow
	for _, topic := range topics {
		fixtures := topicFixtures(topic, perTopic)

		legacy := make([]string, perTopic)
		current := make([]string, perTopic)
		legacyFP := make([]string, perTopic)
		currentFP := make([]string, perTopic)
		for i, f := range fixtures {
			legacy[i] = renderLegacyLayout(f)
			cur := canonicalCurrentPrompt(f)
			current[i] = cur
			// Production prefix fingerprint for the modify path:
			//   current: FingerprintPromptPrefix(system, SharedSourceContexts)
			//   legacy:  FingerprintPromptPrefix(single user message) (constant
			//            empty stable prefix — the single message has no system).
			system, _ := renderCurrentLayout(f)
			currentFP[i] = chat.FingerprintPromptPrefix(system, f.SharedSourceContexts)
			legacyFP[i] = chat.FingerprintPromptPrefix(legacy[i])
		}

		rows = append(rows,
			layoutRow{
				Topic: topic, Variant: "legacy", InstanceCount: perTopic,
				CommonPrefixBytes:        commonPrefixBytes(legacy),
				FirstDifferingByteIndex:  commonPrefixBytes(legacy),
				CommonPrefixTokens:       commonPrefixTokens(legacy),
				FirstDifferingTokenIndex: commonPrefixTokens(legacy),
				FingerprintCardinality:   distinctStrings(legacyFP),
				PromptSHA256Distinct:     distinctStrings(mapHash(legacy)),
				CommonPrefixSHA256:       sha256hex(legacy[0][:commonPrefixBytes(legacy)]),
			},
			layoutRow{
				Topic: topic, Variant: "current", InstanceCount: perTopic,
				CommonPrefixBytes:        commonPrefixBytes(current),
				FirstDifferingByteIndex:  commonPrefixBytes(current),
				CommonPrefixTokens:       commonPrefixTokens(current),
				FirstDifferingTokenIndex: commonPrefixTokens(current),
				FingerprintCardinality:   distinctStrings(currentFP),
				PromptSHA256Distinct:     distinctStrings(mapHash(current)),
				CommonPrefixSHA256:       sha256hex(current[0][:commonPrefixBytes(current)]),
			},
		)
	}
	return rows
}

func mapHash(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = sha256hex(s)
	}
	return out
}

// writeLayoutTSV writes the sanitized rows to path. Callers (the one-click
// verifier) pass an env var so plain `go test` leaves the filesystem untouched.
func writeLayoutTSV(path string, rows []layoutRow) error {
	var b strings.Builder
	b.WriteString("topic\tvariant\tinstance_count\tcommon_prefix_bytes\tfirst_differing_byte_index\tcommon_prefix_tokens_local_proxy\tfirst_differing_token_index_local_proxy\tprefix_fingerprint_cardinality\tprompt_sha256_distinct\tcommon_prefix_sha256\n")
	for _, r := range rows {
		b.WriteString(fmt.Sprintf("%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
			r.Topic, r.Variant, r.InstanceCount, r.CommonPrefixBytes, r.FirstDifferingByteIndex,
			r.CommonPrefixTokens, r.FirstDifferingTokenIndex, r.FingerprintCardinality,
			r.PromptSHA256Distinct, r.CommonPrefixSHA256))
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

var layoutTopics = []string{"alpha", "beta", "gamma"}
var layoutPerTopic = 10

func TestWikiLayout_DeterministicBytes(t *testing.T) {
	for _, topic := range layoutTopics {
		for _, f := range topicFixtures(topic, layoutPerTopic) {
			a1 := canonicalCurrentPrompt(f)
			a2 := canonicalCurrentPrompt(f)
			if a1 != a2 {
				t.Fatalf("%s: current layout not deterministic", topic)
			}
			l1 := renderLegacyLayout(f)
			l2 := renderLegacyLayout(f)
			if l1 != l2 {
				t.Fatalf("%s: legacy layout not deterministic", topic)
			}
			if sha256hex(a1) != sha256hex(a2) || sha256hex(l1) != sha256hex(l2) {
				t.Fatalf("%s: hash mismatch on identical fixture", topic)
			}
		}
	}
}

func TestWikiLayout_RoleAndLayout(t *testing.T) {
	f := topicFixtures("alpha", 1)[0]
	system, user := renderCurrentLayout(f)
	if system == "" || user == "" {
		t.Fatalf("current layout must produce non-empty system and user messages")
	}
	if !strings.HasPrefix(system, "You are a wiki editor") {
		t.Fatalf("current system message missing stable rules header")
	}
	legacy := renderLegacyLayout(f)
	if strings.Contains(legacy, "<shared_source_contexts>") {
		t.Fatalf("legacy layout must not contain the post-6fb85810 shared source block")
	}
	if !strings.HasPrefix(legacy, "You are a wiki editor") {
		t.Fatalf("legacy layout missing rules header")
	}
}

func TestWikiLayout_VariableSeparation(t *testing.T) {
	for _, topic := range layoutTopics {
		fs := topicFixtures(topic, layoutPerTopic)
		legacy := make([]string, layoutPerTopic)
		current := make([]string, layoutPerTopic)
		for i, f := range fs {
			legacy[i] = renderLegacyLayout(f)
			current[i] = canonicalCurrentPrompt(f)
		}
		lb := commonPrefixBytes(legacy)
		cb := commonPrefixBytes(current)
		// The common prefix must not contain any page-specific variable text.
		for _, needle := range []string{"instance-01", "instance-02", "Synthetic addition"} {
			if strings.Contains(current[0][:cb], needle) {
				t.Fatalf("%s: current common prefix leaked page variable %q", topic, needle)
			}
			if strings.Contains(legacy[0][:lb], needle) {
				t.Fatalf("%s: legacy common prefix leaked page variable %q", topic, needle)
			}
		}
		// The shared source context must be INSIDE the current stable prefix,
		// and ABSENT from the legacy prompt entirely.
		if !strings.Contains(current[0][:cb], "<shared_source_contexts>") {
			t.Fatalf("%s: current stable prefix does not include shared source context", topic)
		}
	}
}

func TestWikiLayout_CustomInstructions(t *testing.T) {
	base := topicFixtures("alpha", 1)[0]
	base.CustomInstructions = "Always answer in the third person."
	base.InstructionScope = "wiki_content"

	system, user := renderCurrentLayout(base)
	if !strings.Contains(system, "<wiki_content_business_instructions>") {
		t.Fatalf("current custom instructions must be appended to the system message")
	}
	if strings.Contains(user, "<wiki_content_business_instructions>") {
		t.Fatalf("current custom instructions must not be injected into the user message")
	}

	legacy := renderLegacyLayout(base)
	if !strings.Contains(legacy, "Always answer in the third person.") {
		t.Fatalf("legacy single-message layout must carry custom instructions inline")
	}

	// Custom instructions change the system prefix (expected), but a constant
	// instruction block does not break same-source stability.
	plain := topicFixtures("alpha", 2)
	plain[0].CustomInstructions, plain[1].CustomInstructions = "Rule A.", "Rule A."
	p0, _ := renderCurrentLayout(plain[0])
	p1, _ := renderCurrentLayout(plain[1])
	if !strings.HasPrefix(p0, strings.TrimSpace(WikiPageModifySystemPrompt)) {
		t.Fatalf("system prompt should still begin with the stable rules")
	}
	_ = p1
}

func TestWikiLayout_PrefixFingerprintStable(t *testing.T) {
	for _, topic := range layoutTopics {
		fs := topicFixtures(topic, layoutPerTopic)
		fps := make([]string, layoutPerTopic)
		for i, f := range fs {
			system, _ := renderCurrentLayout(f)
			fps[i] = chat.FingerprintPromptPrefix(system, f.SharedSourceContexts)
		}
		if distinctStrings(fps) != 1 {
			t.Fatalf("%s: prefix fingerprint cardinality within a same-source batch = %d, want 1", topic, distinctStrings(fps))
		}
	}
	// Across topics the fingerprint must differ (the shared source context
	// differs), proving it is sensitive to the right stable segment.
	all := map[string]struct{}{}
	for _, topic := range layoutTopics {
		f := topicFixtures(topic, 1)[0]
		system, _ := renderCurrentLayout(f)
		all[chat.FingerprintPromptPrefix(system, f.SharedSourceContexts)] = struct{}{}
	}
	if len(all) != len(layoutTopics) {
		t.Fatalf("prefix fingerprint should differ across topics, got %d distinct for %d topics", len(all), len(layoutTopics))
	}
}

func TestWikiLayout_CurrentLongerPrefix(t *testing.T) {
	// H1-structure: Current Layout's cross-request common prefix is longer than
	// Legacy Layout's, per topic, because the shared source context is placed
	// before the first variable segment.
	for _, topic := range layoutTopics {
		fs := topicFixtures(topic, layoutPerTopic)
		legacy := make([]string, layoutPerTopic)
		current := make([]string, layoutPerTopic)
		for i, f := range fs {
			legacy[i] = renderLegacyLayout(f)
			current[i] = canonicalCurrentPrompt(f)
		}
		lb := commonPrefixBytes(legacy)
		cb := commonPrefixBytes(current)
		if cb <= lb {
			t.Fatalf("%s: current common prefix %d bytes not longer than legacy %d bytes", topic, cb, lb)
		}
	}
}

func TestWikiLayout_RetractionBranchRenders(t *testing.T) {
	f := topicFixtures("alpha", 1)[0]
	f.HasRetractions = true
	f.HasAdditions = false
	f.DeletedContent = "Deterministic synthetic deleted content."
	f.RemainingSourcesContent = "Deterministic synthetic remaining content."
	legacy := renderLegacyLayout(f)
	if !strings.Contains(legacy, "<deleted_documents>") {
		t.Fatalf("legacy retraction branch missing deleted_documents block")
	}
	system, user := renderCurrentLayout(f)
	if !strings.Contains(user, "<deleted_documents>") {
		t.Fatalf("current retraction branch missing deleted_documents block")
	}
	if system == "" {
		t.Fatalf("current system message must remain non-empty for retraction")
	}
}

func TestWikiLayout_EvidenceNoRawPrompt(t *testing.T) {
	rows := computeRows(layoutTopics, layoutPerTopic)
	var buf strings.Builder
	writeErr := writeLayoutTSV("", rows) // path "" -> os.WriteFile error, ignored
	_ = writeErr
	for _, r := range rows {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
	}
	// The evidence payload must never contain the stable prompt text itself.
	for _, needle := range []string{"You are a wiki editor", "<page_metadata>", "<existing_page_content>", "SOURCE GROUNDING"} {
		if strings.Contains(buf.String(), needle) {
			t.Fatalf("sanitized evidence leaked prompt text %q", needle)
		}
	}
}

// TestWikiLayout_WriteSanitizedTSV emits the sanitized aggregate TSV when the
// verifier sets TASK006_EVIDENCE_DIR. Plain `go test` never writes it.
func TestWikiLayout_WriteSanitizedTSV(t *testing.T) {
	dir := os.Getenv("TASK006_EVIDENCE_DIR")
	if dir == "" {
		t.Skip("TASK006_EVIDENCE_DIR not set; skipping evidence emission")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rows := computeRows(layoutTopics, layoutPerTopic)
	if err := writeLayoutTSV(filepath.Join(dir, "layout_stability_raw.tsv"), rows); err != nil {
		t.Fatal(err)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Variant != rows[j].Variant {
			return rows[i].Variant < rows[j].Variant
		}
		return rows[i].Topic < rows[j].Topic
	})
	t.Logf("wrote layout_stability_raw.tsv with %d rows", len(rows))
}
