package agent

// Task013 (AC-R8 real-provider prompt-cache A/B) — production builder seam tests.
//
// These tests freeze the byte-equivalence between the pre-extraction production
// assembly (reference renderer, validated against generateWithTemplate by
// Task006's layout harness) and the extracted BuildWikiPageModifyMessages.
// The treatment arm of the experiment must use BuildWikiPageModifyMessages; a
// byte drift between reference and builder fails these tests closed.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// layoutFixtureData mirrors renderLayoutTemplate's data map so the reference
// renderer and the extracted builder consume byte-identical inputs.
func layoutFixtureData(f layoutFixture) map[string]string {
	return map[string]string{
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
		"CustomInstructions":      f.CustomInstructions,
		"InstructionScope":        f.InstructionScope,
	}
}

// task013Fixtures builds the 12 synthetic paired-sample fixture set: three size
// tiers (small/medium/large) × 4 pairs. All content is deterministic synthetic
// text with no real-world referent, personal data or credentials. The fixed
// synthetic facts per fixture are the later quality-validator recall targets.
func task013Fixtures() []layoutFixture {
	sizes := []string{"small", "small", "small", "small", "medium", "medium", "medium", "medium", "large", "large", "large", "large"}
	out := make([]layoutFixture, 0, 12)
	shared := "Synthetic domain t013-cache-abi is a non-real fixture domain used solely for the Task013 real-provider prompt-cache A/B. It has no real-world referent and contains no personal, credential or confidential data."
	for i := 1; i <= 12; i++ {
		out = append(out, layoutFixture{
			HasAdditions:         true,
			SharedSourceContexts: shared,
			PageSlug:             fmt.Sprintf("t013-cache-abi/instance-%02d", i),
			PageTitle:            fmt.Sprintf("Synthetic Cache ABI Instance %02d", i),
			PageType:             "synthetic-entity",
			PageAliases:          "",
			ExistingContent:      syntheticBody("t013-cache-abi", i, sizes[i-1]),
			NewContent: fmt.Sprintf(
				"Synthetic addition %d for t013-cache-abi. Fixed fact alpha-%02d: value %d. Fixed fact beta-%02d: value %d. Fixed fact gamma-%02d: value %d.",
				i, i, 1000+i, i, 2000+i, i, 3000+i),
			HasRetractions: false,
			AvailableSlugs: "t013-cache-abi/instance-01, t013-cache-abi/instance-02, t013-cache-abi/instance-03",
			Language:       "English",
		})
	}
	return out
}

func sha256hexBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestTask013_BuilderByteEquivalent(t *testing.T) {
	for _, f := range task013Fixtures() {
		refSystem, refUser := renderCurrentLayout(f)
		msgs, err := BuildWikiPageModifyMessages(layoutFixtureData(f))
		if err != nil {
			t.Fatalf("%s: builder error: %v", f.PageSlug, err)
		}
		if len(msgs) != 2 {
			t.Fatalf("%s: want 2 messages, got %d", f.PageSlug, len(msgs))
		}
		if msgs[0].Role != "system" || msgs[1].Role != "user" {
			t.Fatalf("%s: unexpected role layout %q/%q", f.PageSlug, msgs[0].Role, msgs[1].Role)
		}
		if msgs[0].Content != refSystem {
			t.Fatalf("%s: system message byte drift after extraction", f.PageSlug)
		}
		if msgs[1].Content != refUser {
			t.Fatalf("%s: user message byte drift after extraction", f.PageSlug)
		}
	}
}

func TestTask013_BuilderDeterministic(t *testing.T) {
	for _, f := range task013Fixtures() {
		data := layoutFixtureData(f)
		a, err := BuildWikiPageModifyMessages(data)
		if err != nil {
			t.Fatal(err)
		}
		b, err := BuildWikiPageModifyMessages(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(a) != len(b) {
			t.Fatalf("%s: non-deterministic message count", f.PageSlug)
		}
		for i := range a {
			if a[i].Role != b[i].Role || a[i].Content != b[i].Content {
				t.Fatalf("%s: non-deterministic assembly", f.PageSlug)
			}
		}
	}
}

func TestTask013_BuilderCustomInstructions(t *testing.T) {
	base := task013Fixtures()[0]
	base.CustomInstructions = "Always answer in the third person."
	base.InstructionScope = "wiki_content"

	msgs, err := BuildWikiPageModifyMessages(layoutFixtureData(base))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msgs[0].Content, "<wiki_content_business_instructions>") {
		t.Fatalf("custom instructions must be appended to the system message")
	}
	if strings.Contains(msgs[1].Content, "Always answer in the third person.") {
		t.Fatalf("custom instructions must not leak into the user message")
	}
	refSystem, _ := renderCurrentLayout(base)
	if msgs[0].Content != refSystem {
		t.Fatalf("custom-instruction assembly drifted from reference")
	}
}

// TestTask013_BuilderEquivalenceTSV emits the production_equivalence.tsv when the
// verifier sets TASK013_EQUIV_DIR. Plain `go test` never writes it.
func TestTask013_BuilderEquivalenceTSV(t *testing.T) {
	dir := os.Getenv("TASK013_EQUIV_DIR")
	if dir == "" {
		t.Skip("TASK013_EQUIV_DIR not set; skipping equivalence emission")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("fixture_id\treference_system_sha256\tbuilder_system_sha256\treference_user_sha256\tbuilder_user_sha256\tsystem_bytes\tuser_bytes\tbyte_equal\n")
	for _, f := range task013Fixtures() {
		refSystem, refUser := renderCurrentLayout(f)
		msgs, err := BuildWikiPageModifyMessages(layoutFixtureData(f))
		if err != nil {
			t.Fatal(err)
		}
		equal := refSystem == msgs[0].Content && refUser == msgs[1].Content
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%t\n",
			f.PageSlug,
			sha256hexBytes([]byte(refSystem)), sha256hexBytes([]byte(msgs[0].Content)),
			sha256hexBytes([]byte(refUser)), sha256hexBytes([]byte(msgs[1].Content)),
			len(refSystem), len(refUser), equal)
	}
	path := filepath.Join(dir, "production_equivalence.tsv")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}
