package main

// Task013 deterministic wiki-output quality validator (plan §3.6).
// Pure string/marker checks over the model output; no LLM judge, no network.
// The validator artifact identity is hashed into evaluator_manifest.json and
// must not change after the experiment preregistration.

import (
	"fmt"
	"regexp"
	"strings"
)

const qualityContractVersion = "task013-quality-v1"

// Quality holds the seven frozen fields of plan §3.6.
type Quality struct {
	SchemaFormatPass         bool    `json:"schema_format_pass"`
	SummaryLinePass          bool    `json:"summary_line_pass"`
	ForbiddenInternalHandles int     `json:"forbidden_internal_handle_count"`
	RequiredFactRecall       float64 `json:"required_fact_recall"`
	UnsupportedFactCount     int     `json:"unsupported_fact_count"`
	ValidLinkPass            bool    `json:"valid_link_pass"`
	SourceGroundingPass      bool    `json:"source_grounding_pass"`
}

// chunkHandlePattern matches internal chunk handles ([c003], [c12] …) that the
// production contract forbids in page bodies (prompts_wiki.go system rule 1).
var chunkHandlePattern = regexp.MustCompile(`\[c\d+\]`)

// wikiLinkPattern matches [[slug|name]] references.
var wikiLinkPattern = regexp.MustCompile(`\[\[([^\]|]+)\|([^\]]+)\]\]`)

func summaryLineWords(summaryLine string) int {
	body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(summaryLine), "SUMMARY:"))
	return len(strings.Fields(body))
}

// Validate computes the seven quality fields for one arm's output.
// allowedSlugs is the fixture's valid-link set; ownSlug must not be linked.
func Validate(output string, requiredFacts, forbiddenFacts, allowedSlugs []string, ownSlug string) Quality {
	q := Quality{}

	firstNonEmpty := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			firstNonEmpty = line
			break
		}
	}
	trimmed := strings.TrimSpace(output)
	q.SchemaFormatPass = strings.HasPrefix(strings.TrimSpace(firstNonEmpty), "SUMMARY:") &&
		len(trimmed) > len(strings.TrimSpace(firstNonEmpty)) &&
		strings.Contains(trimmed, "\n")

	if q.SchemaFormatPass {
		w := summaryLineWords(firstNonEmpty)
		q.SummaryLinePass = w >= 15 && w <= 40
	}

	q.ForbiddenInternalHandles = len(chunkHandlePattern.FindAllString(output, -1))

	recalled := 0
	for _, f := range requiredFacts {
		if strings.Contains(output, f) {
			recalled++
		}
	}
	if len(requiredFacts) > 0 {
		q.RequiredFactRecall = float64(recalled) / float64(len(requiredFacts))
	} else {
		q.RequiredFactRecall = 1.0
	}

	for _, f := range forbiddenFacts {
		q.UnsupportedFactCount += strings.Count(output, f)
	}

	allowed := map[string]struct{}{}
	for _, s := range allowedSlugs {
		allowed[s] = struct{}{}
	}
	links := wikiLinkPattern.FindAllStringSubmatch(output, -1)
	linksOK := true
	for _, m := range links {
		target := strings.TrimSpace(m[1])
		if target == ownSlug {
			linksOK = false
			break
		}
		if _, ok := allowed[target]; !ok {
			linksOK = false
			break
		}
	}
	q.ValidLinkPass = linksOK

	q.SourceGroundingPass = q.RequiredFactRecall == 1.0 && q.UnsupportedFactCount == 0
	return q
}

// NonRegressionGate applies plan §3.6 quality gates across the two arms.
func NonRegressionGate(treat, ctrl Quality) error {
	if !treat.SchemaFormatPass || !ctrl.SchemaFormatPass {
		return fmt.Errorf("quality gate FAIL: both arms must have schema_format_pass")
	}
	if !treat.SummaryLinePass || !ctrl.SummaryLinePass {
		return fmt.Errorf("quality gate FAIL: both arms must have summary_line_pass")
	}
	if treat.UnsupportedFactCount != 0 {
		return fmt.Errorf("quality gate FAIL: treatment unsupported_fact_count = %d", treat.UnsupportedFactCount)
	}
	if treat.ForbiddenInternalHandles != 0 {
		return fmt.Errorf("quality gate FAIL: treatment forbidden_internal_handle_count = %d", treat.ForbiddenInternalHandles)
	}
	if treat.RequiredFactRecall < ctrl.RequiredFactRecall-0.05 {
		return fmt.Errorf("quality gate FAIL: treatment recall %.3f < control %.3f - 0.05", treat.RequiredFactRecall, ctrl.RequiredFactRecall)
	}
	if !treat.ValidLinkPass || !ctrl.ValidLinkPass {
		return fmt.Errorf("quality gate FAIL: valid_link_pass must hold in both arms")
	}
	return nil
}

// allowedSlugsFor returns the synthetic slug set of a fixture (the P0 fixtures
// use plain slugs; the harness performs no ref-N handle encoding because the
// experiment controls its own request construction).
func allowedSlugsFor(availableSlugsCSV string) []string {
	var out []string
	for _, s := range strings.Split(availableSlugsCSV, ",") {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}
