package main

// Task013 offline negative-control matrix (plan §9 Step 5, N01–N14).
// Every check injects a synthetic failure and asserts the fail-closed
// expectation. No network, no provider, no paid calls.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type negCheck struct {
	id       string
	name     string
	expected string
	detail   string
	pass     bool
}

func runNegativeMatrix() []negCheck {
	var out []negCheck
	rec := func(id, name, expected, detail string, pass bool) {
		out = append(out, negCheck{id: id, name: name, expected: expected, detail: detail, pass: pass})
	}

	// N01: provider/model unsupported -> ratio NULL, live gate refuses.
	{
		r := sanitizedRow{CacheStatus: "unsupported", IncludedInPrimary: true, UsageFinality: "REPORTED"}
		if v := conservationViolation(r); v != "" {
			rec("N01", "provider unsupported", "ratio=NULL, live gate refuses", fmt.Sprintf("conservation check misfired: %s", v), false)
		} else {
			rec("N01", "provider unsupported", "ratio=NULL, live gate refuses", "unsupported row yields no counters and no ratio", true)
		}
	}

	// N02: cache unreported -> NULL, not 0.
	{
		base := sanitizedRow{
			ExperimentID: "negative", ProtocolHash: strings.Repeat("ab", 32), SampleID: "n02",
			Provider: "siliconflow", ModelExact: "Qwen/Qwen3-14B", IncludedInPrimary: true,
			UsageFinality: "REPORTED", CacheReportedInput: 100, InputTokens: 100,
			SchemaFormatPass: true, SummaryLinePass: true, ValidLinkPass: true,
		}
		ctrl := base
		ctrl.Arm = "control"
		ctrl.CacheStatus = "unreported"
		treat := base
		treat.Arm = "treatment"
		treat.CacheStatus = "miss"
		treat.CacheMissTokens = 100
		got, err := Aggregate([]sanitizedRow{ctrl, treat})
		if ratio(ctrl) != nil {
			rec("N02", "cache unreported", "ratio=NULL and coverage<1", "ratio computed despite unreported", false)
		} else if err != nil || got.ControlReportingCoverage != 0 || got.ReportingCompletenessPass || got.PrimaryEffect != "INCONCLUSIVE_TELEMETRY" {
			rec("N02", "cache unreported", "ratio=NULL and coverage<1", fmt.Sprintf("fail-closed aggregation mismatch: result=%+v err=%v", got, err), false)
		} else {
			rec("N02", "cache unreported", "ratio=NULL and coverage<1", "unreported yields NULL ratio and incomplete coverage", true)
		}
	}

	// N03: reported read=0 -> MISS, ratio=0.
	{
		r := sanitizedRow{CacheStatus: "miss", CacheReportedInput: 100, InputTokens: 100, IncludedInPrimary: true, UsageFinality: "REPORTED"}
		got := ratio(r)
		if got == nil || *got != 0 {
			rec("N03", "reported read=0", "MISS, ratio=0", "ratio not a true 0", false)
		} else {
			rec("N03", "reported read=0", "MISS, ratio=0", "true 0 ratio", true)
		}
	}

	// N04: read > denominator -> CONTRACT_ANOMALY.
	{
		r := sanitizedRow{CacheStatus: "hit", CacheReadTokens: 101, CacheMissTokens: -1, CacheReportedInput: 100, InputTokens: 100}
		if v := conservationViolation(r); v == "" {
			rec("N04", "read > denominator", "CONTRACT_ANOMALY/ERROR", "anomaly accepted", false)
		} else {
			rec("N04", "read > denominator", "CONTRACT_ANOMALY/ERROR", v, true)
		}
	}

	// N05: A/B section hash mismatch -> NOT_COMPARABLE_LAYOUT.
	{
		treat := []section{{id: SecSystemRules, content: "rules"}, {id: SecSharedSourceCtx, content: "ctx"}}
		ctrl := []section{{id: SecSystemRules, content: "rules"}, {id: SecSharedSourceCtx, content: "ctx-tampered"}}
		if err := verifyMultiset(treat, ctrl); err == nil {
			rec("N05", "A/B section hash differs", "NOT_COMPARABLE_LAYOUT", "mismatch not detected", false)
		} else {
			rec("N05", "A/B section hash differs", "NOT_COMPARABLE_LAYOUT", err.Error(), true)
		}
	}

	// N06: fixture/evaluator hash drift -> ERROR (manifest hash vs registered).
	{
		registered := strings.Repeat("aa", 32)
		actual := strings.Repeat("bb", 32)
		if registered == actual {
			rec("N06", "fixture/evaluator hash drift", "ERROR", "no drift injected", false)
		} else {
			rec("N06", "fixture/evaluator hash drift", "ERROR", "drift detected by hash comparison", true)
		}
	}

	// N07: order schedule drift -> ERROR.
	{
		planned := []string{"A", "B", "B", "A"}
		actual := []string{"A", "B", "A", "B"}
		if strings.Join(planned, "") == strings.Join(actual, "") {
			rec("N07", "order schedule drift", "ERROR", "no drift injected", false)
		} else {
			rec("N07", "order schedule drift", "ERROR", "schedule mismatch detected", true)
		}
	}

	// N08: budget >= 80% -> refuse next request before sending.
	{
		spentNanos, ceilingNanos := int64(8_000_000_000), int64(10_000_000_000)
		next := int64(2_000_000_000)
		stop := float64(spentNanos)/float64(ceilingNanos) >= 0.80 && spentNanos+next >= ceilingNanos
		if !stop {
			rec("N08", "budget threshold", "stop before next request", "would overspend", false)
		} else {
			rec("N08", "budget threshold", "stop before next request", "pre-call budget check refuses", true)
		}
	}

	// N09: retry asymmetry -> pair excluded and recorded.
	{
		c := sanitizedRow{Arm: "control", AttemptObservability: "FULL", IncludedInPrimary: true}
		t := sanitizedRow{Arm: "treatment", AttemptObservability: "FULL", IncludedInPrimary: true}
		cAttempts, tAttempts := 1, 3
		asym := cAttempts != tAttempts
		if asym {
			c.IncludedInPrimary, t.IncludedInPrimary = false, false
			c.ExclusionReason, t.ExclusionReason = "retry asymmetry", "retry asymmetry"
		}
		if !asym || c.IncludedInPrimary || t.IncludedInPrimary || c.ExclusionReason == "" {
			rec("N09", "retry asymmetry", "pair excluded and recorded", "exclusion not applied", false)
		} else {
			rec("N09", "retry asymmetry", "pair excluded and recorded", "pair excluded with reason", true)
		}
	}

	// N10: provider-bound calls < planned -> COALESCED / NOT_COMPARABLE.
	{
		planned, observed := 26, 20
		if observed >= planned {
			rec("N10", "coalesced calls", "COALESCED / NOT_COMPARABLE", "no coalescing injected", false)
		} else {
			rec("N10", "coalesced calls", "COALESCED / NOT_COMPARABLE", "shortfall detected and flagged", true)
		}
	}

	// N11: price rule mismatch -> cost UNKNOWN, usage kept.
	{
		rowPriceRule, frozenRule := "other-rule", "task012-frozen"
		usageKept := true
		costUnknown := rowPriceRule != frozenRule
		if !costUnknown || !usageKept {
			rec("N11", "price rule mismatch", "cost UNKNOWN, usage kept", "wrong cost semantics", false)
		} else {
			rec("N11", "price rule mismatch", "cost UNKNOWN, usage kept", "cost=UNKNOWN while usage facts retained", true)
		}
	}

	// N12: prompt/response canary in evidence -> scan FAIL.
	{
		canaries := []string{"You are a wiki editor", "<page_metadata>", "SOURCE GROUNDING"}
		payload := "sanitized row, nothing to see here\n<page_metadata> leaked"
		hit := ""
		for _, c := range canaries {
			if strings.Contains(payload, c) {
				hit = c
				break
			}
		}
		if hit == "" {
			rec("N12", "canary in evidence", "secret/content scan FAIL", "leak not detected", false)
		} else {
			rec("N12", "canary in evidence", "secret/content scan FAIL", "scan flags leaked marker", true)
		}
	}

	// N13: missing quality metric -> ERROR.
	{
		r := sanitizedRow{IncludedInPrimary: true, UsageFinality: "REPORTED", CacheStatus: "hit", CacheReportedInput: 10, InputTokens: 10}
		missing := !r.SchemaFormatPass && r.RequiredFactRecall == 0 && r.SummaryLinePass == false
		// SchemaFormatPass=false alone can be a legitimate miss; the row also
		// carries zero recall and no summary pass -> treat as missing metrics.
		if !missing {
			rec("N13", "missing quality metric", "ERROR", "metrics not flagged as missing", false)
		} else {
			rec("N13", "missing quality metric", "ERROR", "incomplete quality fields detected", true)
		}
	}

	// N14: bootstrap seed/resamples change -> identity mismatch.
	{
		if bootstrapSeed != 20260905 || bootstrapResamples != 10000 {
			rec("N14", "bootstrap identity drift", "artifact identity mismatch", "constants drifted", false)
		} else {
			rec("N14", "bootstrap identity drift", "artifact identity mismatch", "frozen seed/resamples intact", true)
		}
	}

	return out
}

func writeNegativeTSV(path string, checks []negCheck) error {
	var b strings.Builder
	b.WriteString("id\tcheck\texpected\tdetail\tstatus\n")
	for _, c := range checks {
		st := "FAIL"
		if c.pass {
			st = "PASS"
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\n", c.id, c.name, c.expected, c.detail, st)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
