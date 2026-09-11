package metric

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Task016 Step 4 / W2 — Metric Differential parity against the pinned reference
// evaluator ranx 0.3.20.
//
// The 10,000 fixed-seed reference vectors are frozen in
// testdata/metric_differential.tsv (SHA-256 locked below). Each row carries the
// unique retrieved ranking, the relevant set and the six reference metric values
// computed with ranx 0.3.20's exact formulas. The Go implementation must match
// every value to <= 1e-12; 0 unexplained mismatch (W2 threshold).

const frozenDifferentialSHA256 = "926a636a815fbae9512276fb19d5544f1adc546bf3e39da0f91815c8913edf34"

func TestMetricDifferentialAgainstRanx020(t *testing.T) {
	path := "testdata/metric_differential.tsv"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != frozenDifferentialSHA256 {
		t.Fatalf("differential vectors SHA-256 = %s, frozen = %s (regenerate or re-freeze)", got, frozenDifferentialSHA256)
	}

	var mismatches int
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		fields := strings.Split(sc.Text(), "\t")
		if len(fields) != 9 {
			t.Fatalf("line %d: expected 9 columns, got %d", line, len(fields))
		}
		relevant := parseIDs(t, fields[1])
		retrieved := parseIDs(t, fields[2])
		want := [6]float64{
			parseFloat(t, fields[3]),
			parseFloat(t, fields[4]),
			parseFloat(t, fields[5]),
			parseFloat(t, fields[6]),
			parseFloat(t, fields[7]),
			parseFloat(t, fields[8]),
		}

		got := [6]float64{
			PrecisionAtK(retrieved, relevant, 10),
			RecallAtK(retrieved, relevant, 10),
			ReciprocalRankAtK(retrieved, relevant, 10),
			AveragePrecision(retrieved, relevant),
			NDCGAtK(retrieved, relevant, 3),
			NDCGAtK(retrieved, relevant, 10),
		}

		for i := range got {
			if math.Abs(got[i]-want[i]) > 1e-12 {
				mismatches++
				t.Logf("line %d metric %d: got %.17g want %.17g (relevant=%v retrieved=%v)",
					line, i, got[i], want[i], relevant, retrieved)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if line != 10000 {
		t.Fatalf("expected 10000 cases, got %d", line)
	}
	if mismatches != 0 {
		t.Fatalf("%d unexplained mismatches (threshold 0)", mismatches)
	}
}

func parseIDs(t *testing.T, s string) []int {
	t.Helper()
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("bad id %q: %v", p, err)
		}
		out = append(out, n)
	}
	return out
}

func parseFloat(t *testing.T, s string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("bad float %q: %v", s, err)
	}
	return f
}
