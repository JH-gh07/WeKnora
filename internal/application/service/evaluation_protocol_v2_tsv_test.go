package service

import (
	"fmt"
	"os"
	"strconv"
	"testing"
)

// TestGenerateIdentityMutationTSV emits the W3 identity_mutation.tsv evidence.
// It is env-gated so a normal `go test ./...` never writes files; the frozen TSV
// is produced by:
//
//	TASK016_MUTATION_TSV_OUT=/path/to/identity_mutation.tsv \
//	  go test -count=1 -run TestGenerateIdentityMutationTSV ./internal/application/service/
//
// The rows are deterministic (fixed base input + ordered mutation cases), so the
// TSV SHA-256 is stable and can be frozen by the verifier.
func TestGenerateIdentityMutationTSV(t *testing.T) {
	out := os.Getenv("TASK016_MUTATION_TSV_OUT")
	if out == "" {
		t.Skip("set TASK016_MUTATION_TSV_OUT to emit identity_mutation.tsv")
	}

	base, err := buildProtocolV2(baseProtocolV2Input())
	if err != nil {
		t.Fatalf("build base: %v", err)
	}

	var sb []byte
	header := "field\tclass\tquality_hash_changed\tperformance_hash_changed\tprovenance_hash_changed\tquality_comparability\tperformance_comparability\tpass\n"
	sb = append(sb, header...)

	allPass := true
	for _, c := range protocolV2MutationCases() {
		in := baseProtocolV2Input()
		c.apply(&in)
		mut, err := buildProtocolV2(in)
		if err != nil {
			t.Fatalf("%s: build: %v", c.name, err)
		}
		qChanged := mut.QualityHash != base.QualityHash
		pChanged := mut.PerformanceHash != base.PerformanceHash
		rChanged := mut.ProvenanceHash != base.ProvenanceHash
		qStatus := CompareQualityProtocol(base, mut).Status
		pStatus := ComparePerformanceProtocol(base, mut).Status

		pass := false
		switch c.class {
		case "QUALITY":
			pass = qChanged && qStatus != Comparable
		case "PERFORMANCE":
			pass = pChanged && !qChanged && pStatus == NotComparablePerformance && qStatus == Comparable
		case "PROVENANCE":
			pass = rChanged && !qChanged && !pChanged && qStatus == Comparable && pStatus == Comparable
		}
		if !pass {
			allPass = false
		}

		sb = append(sb, []byte(fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			c.name, c.class,
			b(qChanged), b(pChanged), b(rChanged),
			qStatus, pStatus, b(pass)))...)
	}

	if !allPass {
		t.Fatalf("one or more mutation rows failed the pre-registered classification")
	}
	if err := os.WriteFile(out, sb, 0o644); err != nil {
		t.Fatalf("write %s: %v", out, err)
	}
}

func b(v bool) string {
	return strconv.FormatBool(v)
}
