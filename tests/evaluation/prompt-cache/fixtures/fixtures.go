// Package fixtures holds the Task013 synthetic paired-sample fixtures
// (AC-R8 real-provider Wiki prompt-cache A/B). It is deliberately
// dependency-free (no WeKnora imports) so both the production seam tests
// (internal/agent) and the experiment harness (tests/evaluation/prompt-cache)
// consume the same frozen data without import cycles.
//
// All content is deterministic synthetic text with no real-world referent,
// personal data, credentials or internal business data (plan §1.3 assumption 8).
package fixtures

import "fmt"

// Cohort markers identify experiment cohorts inside the synthetic shared
// source context. They carry no business semantics and are byte-identical
// across the A/B arms (plan §5.2).
const (
	CohortMarkerMain  = "wk-task013-ab-v1"
	CohortMarkerPilot = "wk-task013-pilot-v1"
)

// Fixture is one synthetic wiki page-modify sample. The data map it produces
// is exactly the template-data shape generateWithTemplate renders.
type Fixture struct {
	ID                      string
	Tier                    string // small|medium|large
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
	// RequiredFacts / ForbiddenFacts drive the deterministic quality
	// validator (plan §3.6). They are synthetic numbered facts.
	RequiredFacts  []string
	ForbiddenFacts []string
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// DataMap renders the template-data map generateWithTemplate / the production
// builder consume. Booleans use "true"/"false" like production callers.
func (f Fixture) DataMap() map[string]string {
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

func sharedContext(domain, cohort string) string {
	return fmt.Sprintf(
		"Synthetic domain %q is a non-real fixture domain used solely for the Task013 real-provider prompt-cache A/B. It has no real-world referent and contains no personal, credential or confidential data.\n<cohort>%s</cohort>",
		domain, cohort,
	)
}

// syntheticBody returns a deterministic, synthetic markdown body of the given
// size tier (same tier shapes as the Task006 layout fixtures).
func syntheticBody(domain string, i int, tier string) string {
	repeat := map[string]int{"small": 2, "medium": 5, "large": 12}[tier]
	var b []byte
	b = append(b, fmt.Sprintf("# Synthetic %s instance %02d\n\n", domain, i)...)
	for r := 0; r < repeat; r++ {
		b = append(b, fmt.Sprintf("This is deterministic synthetic sentence %d for %s instance %02d. It exists only to vary body length for prefix-stability measurement.\n", r, domain, i)...)
	}
	return string(b)
}

func newContent(i int) string {
	return fmt.Sprintf(
		"Synthetic addition %d for t013-cache-abi. Fixed fact alpha-%02d: value %d. Fixed fact beta-%02d: value %d. Fixed fact gamma-%02d: value %d.",
		i, i, 1000+i, i, 2000+i, i, 3000+i)
}

func facts(i int) (required, forbidden []string) {
	required = []string{
		fmt.Sprintf("Fixed fact alpha-%02d: value %d", i, 1000+i),
		fmt.Sprintf("Fixed fact beta-%02d: value %d", i, 2000+i),
		fmt.Sprintf("Fixed fact gamma-%02d: value %d", i, 3000+i),
	}
	forbidden = []string{
		"value 9999",
		"Synthetic hallucinated claim not present in any source",
	}
	return required, forbidden
}

// All returns the 12 frozen paired-sample fixtures: three size tiers
// (small/medium/large) × 4 pairs, main cohort.
func All() []Fixture {
	domain := "t013-cache-abi"
	tiers := []string{"small", "small", "small", "small", "medium", "medium", "medium", "medium", "large", "large", "large", "large"}
	out := make([]Fixture, 0, 12)
	for i := 1; i <= 12; i++ {
		required, forbidden := facts(i)
		out = append(out, Fixture{
			ID:                      fmt.Sprintf("%s/instance-%02d", domain, i),
			Tier:                    tiers[i-1],
			HasAdditions:            true,
			SharedSourceContexts:    sharedContext(domain, CohortMarkerMain),
			PageSlug:                fmt.Sprintf("%s/instance-%02d", domain, i),
			PageTitle:               fmt.Sprintf("Synthetic Cache ABI Instance %02d", i),
			PageType:                "synthetic-entity",
			PageAliases:             "",
			ExistingContent:         syntheticBody(domain, i, tiers[i-1]),
			NewContent:              newContent(i),
			HasRetractions:          false,
			DeletedContent:          "",
			RemainingSourcesContent: "",
			AvailableSlugs:          fmt.Sprintf("%s/instance-01, %s/instance-02, %s/instance-03", domain, domain, domain),
			Language:                "English",
			CustomInstructions:      "",
			InstructionScope:        "wiki_content",
			RequiredFacts:           required,
			ForbiddenFacts:          forbidden,
		})
	}
	return out
}

// PilotPair returns two pilot fixtures using the independent pilot cohort.
// Pilot results never enter the main A/B dataset (plan §5.2 / Step 6).
func PilotPair() []Fixture {
	domain := "t013-cache-pilot"
	out := make([]Fixture, 0, 2)
	for i := 1; i <= 2; i++ {
		required, forbidden := facts(900 + i)
		out = append(out, Fixture{
			ID:                      fmt.Sprintf("%s/instance-%02d", domain, i),
			Tier:                    "medium",
			HasAdditions:            true,
			SharedSourceContexts:    sharedContext(domain, CohortMarkerPilot),
			PageSlug:                fmt.Sprintf("%s/instance-%02d", domain, i),
			PageTitle:               fmt.Sprintf("Synthetic Pilot Instance %02d", i),
			PageType:                "synthetic-entity",
			PageAliases:             "",
			ExistingContent:         syntheticBody(domain, i, "medium"),
			NewContent:              fmt.Sprintf("Synthetic pilot addition %d. Fixed fact alpha-%02d: value %d.", i, 900+i, 1900+i),
			HasRetractions:          false,
			DeletedContent:          "",
			RemainingSourcesContent: "",
			AvailableSlugs:          fmt.Sprintf("%s/instance-01, %s/instance-02", domain, domain),
			Language:                "English",
			CustomInstructions:      "",
			InstructionScope:        "wiki_content",
			RequiredFacts:           required,
			ForbiddenFacts:          forbidden,
		})
	}
	return out
}
