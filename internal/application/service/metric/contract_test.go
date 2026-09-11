package metric

import (
	"regexp"
	"testing"
)

func TestMeasurementContractVersionNotUnversioned(t *testing.T) {
	if MeasurementContractVersion == "" || MeasurementContractVersion == "UNVERSIONED" {
		t.Fatalf("version = %q, must be non-empty and non-UNVERSIONED", MeasurementContractVersion)
	}
}

func TestMeasurementContractHashNonEmptyAndStable(t *testing.T) {
	first := MeasurementContractHash()
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first) {
		t.Fatalf("hash %q is not 64 lowercase hex", first)
	}
	for i := 0; i < 100; i++ {
		if MeasurementContractHash() != first {
			t.Fatalf("hash not stable across serializations (iter %d)", i)
		}
	}
}

func TestMeasurementContractHashFrozen(t *testing.T) {
	// F05: any change to standard.go (or the frozen definitions) MUST change this
	// hash. This test freezes the current value so an accidental edit is caught.
	//
	// Hash history:
	//   5328ee91… — initial standard.go (Precision@k denominator was |retrieved@k|).
	//   11cb4167… — Step 4: Precision@k denominator corrected to the fixed cutoff k
	//                (|retrieved@k ∩ relevant| / k), matching the frozen
	//                preregistration formula AND the pinned ranx 0.3.20 reference.
	const frozen = "11cb4167a48182feaa9411e6f083946cabe613c7c6fb3c2ad6601259d48c7ac7"
	if got := MeasurementContractHash(); got != frozen {
		t.Fatalf("contract hash = %s, frozen = %s (implementation changed -> new contract hash required)", got, frozen)
	}
}
