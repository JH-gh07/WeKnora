package types

import (
	"reflect"
	"testing"
)

// TestLegacyStandardNoJSONFieldCollision enforces AC07: the two non-standard
// legacy metric field names (Precision, MAP) must never collide with a standard
// measurement-contract/v1 field name. A collision would let a legacy number be
// read as a standard number and enter a blocking comparison. (mrr is shared
// intentionally because legacy and standard reciprocal-rank share semantics.)
func TestLegacyStandardNoJSONFieldCollision(t *testing.T) {
	legacy := jsonFieldNames(t, RetrievalMetrics{})
	standard := jsonFieldNames(t, StandardRetrievalMetrics{})

	legacySet := make(map[string]struct{}, len(legacy))
	for _, name := range legacy {
		legacySet[name] = struct{}{}
	}
	standardSet := make(map[string]struct{}, len(standard))
	for _, name := range standard {
		standardSet[name] = struct{}{}
	}

	// The two non-standard legacy metrics must not keep their old unprefixed
	// names (precision / map), which would collide with standard map.
	for _, forbidden := range []string{"precision", "map"} {
		if _, ok := legacySet[forbidden]; ok {
			t.Fatalf("legacy metric must not expose unprefixed %q (Decision 016-4)", forbidden)
		}
	}
	// The renamed legacy names must not collide with any standard field.
	for _, renamed := range []string{"legacy_nonstandard_precision", "legacy_nonstandard_map"} {
		if _, ok := standardSet[renamed]; ok {
			t.Fatalf("standard metrics must not expose %q", renamed)
		}
	}
}

// TestLegacyMetricsAreExplicitlyPrefixed verifies that the non-standard legacy
// fields (Precision, MAP) carry the legacy_nonstandard_ prefix (Decision 016-4).
func TestLegacyMetricsAreExplicitlyPrefixed(t *testing.T) {
	legacy := jsonFieldNames(t, RetrievalMetrics{})
	want := map[string]bool{
		"legacy_nonstandard_precision": false,
		"legacy_nonstandard_map":       false,
	}
	for _, name := range legacy {
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("expected legacy field %q to be present and prefixed; got fields %v", name, legacy)
		}
	}
}

// TestStandardMetricJSONNames verifies the standard metric JSON names are the
// frozen measurement-contract/v1 names (not the legacy names).
func TestStandardMetricJSONNames(t *testing.T) {
	standard := jsonFieldNames(t, StandardRetrievalMetrics{})
	required := []string{"precision_at_10", "recall_at_10", "mrr", "ap", "map", "ndcg_at_3", "ndcg_at_10"}
	got := make(map[string]bool, len(standard))
	for _, name := range standard {
		got[name] = true
	}
	for _, name := range required {
		if !got[name] {
			t.Fatalf("standard metric %q missing; got %v", name, standard)
		}
	}
}

// jsonFieldNames returns the JSON field names of a struct value in declaration
// order, ignoring nested struct/omitempty options (top-level fields only).
func jsonFieldNames(t *testing.T, v any) []string {
	t.Helper()
	typ := reflect.TypeOf(v)
	var names []string
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" {
			continue
		}
		name := tag
		for j := 0; j < len(name); j++ {
			if name[j] == ',' {
				name = name[:j]
				break
			}
		}
		names = append(names, name)
	}
	return names
}
