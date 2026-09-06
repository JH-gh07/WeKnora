package agent

// Task013 (AC-R8 real-provider prompt-cache A/B) — production builder seam tests.
//
// These tests freeze the byte-equivalence between the pre-extraction production
// assembly (reference renderer, validated against generateWithTemplate by
// Task006's layout harness) and the extracted BuildWikiPageModifyMessages.
// The treatment arm of the experiment must use BuildWikiPageModifyMessages; a
// byte drift between reference and builder fails these tests closed.
//
// Fixture data comes from tests/evaluation/prompt-cache/fixtures, the same
// frozen source the experiment harness consumes.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/tests/evaluation/prompt-cache/fixtures"
)

// fixtureToLayout adapts the frozen fixtures.Fixture to the Task006 test-local
// layoutFixture so the reference renderer consumes identical values.
func fixtureToLayout(f fixtures.Fixture) layoutFixture {
	return layoutFixture{
		HasAdditions:            f.HasAdditions,
		SharedSourceContexts:    f.SharedSourceContexts,
		PageSlug:                f.PageSlug,
		PageTitle:               f.PageTitle,
		PageType:                f.PageType,
		PageAliases:             f.PageAliases,
		ExistingContent:         f.ExistingContent,
		NewContent:              f.NewContent,
		HasRetractions:          f.HasRetractions,
		DeletedContent:          f.DeletedContent,
		RemainingSourcesContent: f.RemainingSourcesContent,
		AvailableSlugs:          f.AvailableSlugs,
		Language:                f.Language,
		CustomInstructions:      f.CustomInstructions,
		InstructionScope:        f.InstructionScope,
	}
}

func sha256hexBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestTask013_BuilderByteEquivalent(t *testing.T) {
	for _, f := range fixtures.All() {
		lf := fixtureToLayout(f)
		refSystem, refUser := renderCurrentLayout(lf)
		msgs, err := BuildWikiPageModifyMessages(f.DataMap())
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
	for _, f := range fixtures.All() {
		data := f.DataMap()
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
	base := fixtures.All()[0]
	base.CustomInstructions = "Always answer in the third person."
	base.InstructionScope = "wiki_content"

	msgs, err := BuildWikiPageModifyMessages(base.DataMap())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msgs[0].Content, "<wiki_content_business_instructions>") {
		t.Fatalf("custom instructions must be appended to the system message")
	}
	if strings.Contains(msgs[1].Content, "Always answer in the third person.") {
		t.Fatalf("custom instructions must not leak into the user message")
	}
	refSystem, _ := renderCurrentLayout(fixtureToLayout(base))
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
	for _, f := range fixtures.All() {
		lf := fixtureToLayout(f)
		refSystem, refUser := renderCurrentLayout(lf)
		msgs, err := BuildWikiPageModifyMessages(f.DataMap())
		if err != nil {
			t.Fatal(err)
		}
		equal := refSystem == msgs[0].Content && refUser == msgs[1].Content
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%t\n",
			f.ID,
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
