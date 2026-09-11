package metric

import (
	"math"
	"math/rand"
	"testing"
)

// Task016 Step 4 — standard metric golden + boundary tests (W0/W2 style).

func close(a, b float64) bool {
	if a == b {
		return true
	}
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}

func TestPrecisionAtKGolden(t *testing.T) {
	cases := []struct {
		name      string
		retrieved []int
		relevant  []int
		k         int
		want      float64
	}{
		// Precision@k denominator is the FIXED cutoff k (ranx semantics), so a
		// perfect-but-short list of 3 with k=10 is 3/10, not 1.0.
		{"perfect short list k10", []int{1, 3, 5}, []int{1, 3, 5}, 10, 3.0 / 10.0},
		{"full cutoff perfect", []int{1, 3, 5}, []int{1, 3, 5}, 3, 1.0},
		{"half hits k10", []int{1, 4, 2}, []int{1, 2, 3}, 10, 2.0 / 10.0},
		{"empty retrieved", []int{}, []int{1, 2}, 10, 0.0},
		{"dedup reduces hits not denominator", []int{1, 1, 2}, []int{1}, 10, 1.0 / 10.0},
		{"cutoff excludes hit", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, []int{11}, 3, 0.0},
		{"empty relevant ok for precision", []int{1, 2}, []int{}, 10, 0.0},
		{"k zero", []int{1}, []int{1}, 0, 0.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PrecisionAtK(c.retrieved, c.relevant, c.k)
			if !close(got, c.want) {
				t.Fatalf("PrecisionAtK = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRecallAtKGolden(t *testing.T) {
	cases := []struct {
		name      string
		retrieved []int
		relevant  []int
		k         int
		want      float64
	}{
		{"perfect", []int{1, 3, 5}, []int{1, 3, 5}, 10, 1.0},
		{"one of three", []int{1}, []int{1, 2, 3}, 10, 1.0 / 3.0},
		{"empty relevant", []int{1}, []int{}, 10, 0.0},
		{"empty retrieved", []int{}, []int{1, 2}, 10, 0.0},
		{"cutoff limits recall", []int{11, 1, 2}, []int{1, 2, 3}, 1, 0.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RecallAtK(c.retrieved, c.relevant, c.k)
			if !close(got, c.want) {
				t.Fatalf("RecallAtK = %v, want %v", got, c.want)
			}
		})
	}
}

func TestReciprocalRankAtKGolden(t *testing.T) {
	cases := []struct {
		name      string
		retrieved []int
		relevant  []int
		k         int
		want      float64
	}{
		{"first rank", []int{1, 2}, []int{1}, 10, 1.0},
		{"second rank", []int{4, 1, 5}, []int{1}, 10, 0.5},
		{"none", []int{4, 5}, []int{1}, 10, 0.0},
		{"cutoff excludes", []int{4, 1}, []int{1}, 1, 0.0},
		{"dedup keeps first", []int{1, 1, 2}, []int{2}, 10, 0.5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ReciprocalRankAtK(c.retrieved, c.relevant, c.k)
			if !close(got, c.want) {
				t.Fatalf("RR = %v, want %v", got, c.want)
			}
		})
	}
}

func TestAveragePrecisionGolden(t *testing.T) {
	cases := []struct {
		name      string
		retrieved []int
		relevant  []int
		want      float64
	}{
		{"perfect", []int{2, 4, 6}, []int{2, 4, 6}, 1.0},
		{"partial", []int{2, 5, 1, 3}, []int{1, 2, 3}, 0.8055555555555555},
		{"none", []int{3, 4}, []int{1, 2}, 0.0},
		{"empty relevant", []int{1}, []int{}, 0.0},
		{"dedup", []int{1, 1, 2}, []int{1, 2}, 1.0}, // after dedup [1,2], both relevant at ranks 1,2 => AP=1
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AveragePrecision(c.retrieved, c.relevant)
			if !close(got, c.want) {
				t.Fatalf("AP = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMeanAveragePrecisionGolden(t *testing.T) {
	ranked := map[int][]int{
		1: {1, 2},
		2: {3, 4},
	}
	relevant := map[int][]int{
		1: {1, 2},
		2: {3},
	}
	// q1 AP = (1/1 + 2/2)/2 = 1.0; q2 AP = (1/1)/1 = 1.0 => MAP = 1.0
	if got := MeanAveragePrecision(ranked, relevant); !close(got, 1.0) {
		t.Fatalf("MAP = %v, want 1.0", got)
	}
}

func TestNDCGAtKGolden(t *testing.T) {
	cases := []struct {
		name      string
		retrieved []int
		relevant  []int
		k         int
		want      float64
	}{
		{"perfect", []int{1, 3, 5}, []int{1, 3, 5}, 10, 1.0},
		{"none", []int{4, 5, 6}, []int{1, 2}, 10, 0.0},
		{"empty relevant", []int{1}, []int{}, 10, 0.0},
		{"empty retrieved", []int{}, []int{1, 2}, 10, 0.0},
		{"cutoff", []int{1, 3, 5}, []int{1, 3, 5}, 2, 1.0}, // only first 2, both relevant
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NDCGAtK(c.retrieved, c.relevant, c.k)
			if !close(got, c.want) {
				t.Fatalf("NDCG = %v, want %v", got, c.want)
			}
		})
	}

	// A non-trivial NDCG hand-computed case: retrieved [4,1,5,2], relevant {1,2}.
	// DCG = 1/log2(3) + 1/log2(5); IDCG = 1/log2(2) + 1/log2(3).
	dcg := 1.0/math.Log2(3.0) + 1.0/math.Log2(5.0)
	idcg := 1.0/math.Log2(2.0) + 1.0/math.Log2(3.0)
	want := dcg / idcg
	if got := NDCGAtK([]int{4, 1, 5, 2}, []int{1, 2}, 10); !close(got, want) {
		t.Fatalf("NDCG = %v, want %v", got, want)
	}
}

// TestStandardMetricsInvariants verifies range and monotonic invariants across
// many deterministic pseudo-random rankings (mutation-style sanity, W2 seed).
func TestStandardMetricsInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(161803399))
	for i := 0; i < 5000; i++ {
		n := rng.Intn(20) + 1
		retrieved := make([]int, n)
		for j := range retrieved {
			retrieved[j] = rng.Intn(30)
		}
		m := rng.Intn(10)
		relevant := make([]int, m)
		for j := range relevant {
			relevant[j] = rng.Intn(30)
		}
		checks := []struct {
			name string
			got  float64
		}{
			{"precision", PrecisionAtK(retrieved, relevant, 10)},
			{"recall", RecallAtK(retrieved, relevant, 10)},
			{"rr", ReciprocalRankAtK(retrieved, relevant, 10)},
			{"ap", AveragePrecision(retrieved, relevant)},
			{"ndcg3", NDCGAtK(retrieved, relevant, 3)},
			{"ndcg10", NDCGAtK(retrieved, relevant, 10)},
		}
		for _, c := range checks {
			if math.IsNaN(c.got) || c.got < 0 || c.got > 1.0 {
				t.Fatalf("iter %d %s out of [0,1]: %v", i, c.name, c.got)
			}
		}
	}
}
