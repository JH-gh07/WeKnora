package pricing

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

func validWire() catalogWire {
	return catalogWire{
		SchemaVersion:    SchemaVersionV1,
		CatalogID:        "siliconflow-public-chat",
		CatalogVersion:   "2026-09-06.1",
		Provider:         "siliconflow",
		Currency:         "CNY",
		SourceURL:        "https://siliconflow.cn/pricing",
		SourceCapturedAt: "2026-09-06T17:40:34Z",
		ReviewAfter:      "2026-10-06T00:00:00Z",
		Rules: []ruleWire{{
			RuleID:          "siliconflow:Qwen/Qwen3-14B:chat:2026-09-06",
			ModelExact:      "Qwen/Qwen3-14B",
			Operation:       "chat",
			ValidFrom:       "2026-09-06T17:40:34Z",
			ValidTo:         "2026-10-06T00:00:00Z",
			BillingUnit:     BillingUnitPer1MTokens,
			InputPrice:      "0.5",
			OutputPrice:     "2",
			CacheReadPrice:  nil,
			CacheWritePrice: nil,
		}},
	}
}

func mustJSON(t *testing.T, w catalogWire) []byte {
	t.Helper()
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestLoadCatalogValid(t *testing.T) {
	c, err := LoadCatalog(mustJSON(t, validWire()))
	if err != nil {
		t.Fatalf("valid catalog must load: %v", err)
	}
	if c.CatalogID != "siliconflow-public-chat" {
		t.Fatalf("unexpected catalog id %q", c.CatalogID)
	}
	if c.Provider != "siliconflow" || c.Currency != "CNY" {
		t.Fatalf("unexpected provider/currency %q/%q", c.Provider, c.Currency)
	}
	if c.Hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if len(c.rules) != 1 || c.rules[0].inputNanos != 500_000_000 || c.rules[0].outputNanos != 2_000_000_000 {
		t.Fatalf("unexpected parsed rules: %+v", c.rules)
	}
}

func TestLoadCatalogUnknownFieldRejected(t *testing.T) {
	b := mustJSON(t, validWire())
	// Inject an unknown top-level field.
	b = []byte(strings.Replace(string(b), `"catalog_id"`, `"catalog_idd"`, 1))
	if _, err := LoadCatalog(b); err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
}

func TestLoadCatalogInvalidCurrency(t *testing.T) {
	for _, bad := range []string{"", "cn", "cny", "CN", "USDD", "12A", "C N"} {
		w := validWire()
		w.Currency = bad
		if _, err := LoadCatalog(mustJSON(t, w)); err == nil {
			t.Fatalf("expected invalid currency %q rejection", bad)
		}
	}
}

func TestLoadCatalogNegativePrice(t *testing.T) {
	w := validWire()
	w.Rules[0].InputPrice = "-0.5"
	if _, err := LoadCatalog(mustJSON(t, w)); err == nil {
		t.Fatal("expected negative price rejection")
	}
}

func TestLoadCatalogMalformedDecimal(t *testing.T) {
	for _, bad := range []string{"", "abc", "1.2.3", "1e3", "0x10", "1/3", "+0.5"} {
		w := validWire()
		w.Rules[0].InputPrice = bad
		if _, err := LoadCatalog(mustJSON(t, w)); err == nil {
			t.Fatalf("expected malformed decimal %q rejection", bad)
		}
	}
}

func TestLoadCatalogOverlappingIntervals(t *testing.T) {
	w := validWire()
	w.Rules = append(w.Rules, ruleWire{
		RuleID:          "siliconflow:Qwen/Qwen3-14B:chat:v2",
		ModelExact:      "Qwen/Qwen3-14B",
		Operation:       "chat",
		ValidFrom:       "2026-09-20T00:00:00Z",
		ValidTo:         "2026-11-01T00:00:00Z",
		BillingUnit:     BillingUnitPer1MTokens,
		InputPrice:      "0.6",
		OutputPrice:     "2.5",
		CacheReadPrice:  nil,
		CacheWritePrice: nil,
	})
	if _, err := LoadCatalog(mustJSON(t, w)); err == nil {
		t.Fatal("expected overlapping interval rejection")
	}
}

func TestLoadCatalogNonOfficialURL(t *testing.T) {
	w := validWire()
	w.SourceURL = "http://evil.example.com/pricing"
	if _, err := LoadCatalog(mustJSON(t, w)); err == nil {
		t.Fatal("expected non-HTTPS rejection")
	}
	w.SourceURL = "https://evil.example.com/pricing"
	if _, err := LoadCatalog(mustJSON(t, w)); err == nil {
		t.Fatal("expected non-official domain rejection")
	}
}

func TestLoadCatalogCapturedAfterValidFrom(t *testing.T) {
	w := validWire()
	// captured after valid_from violates source_captured_at <= valid_from.
	w.Rules[0].ValidFrom = "2026-09-06T00:00:00Z"
	if _, err := LoadCatalog(mustJSON(t, w)); err == nil {
		t.Fatal("expected captured-after-valid_from rejection")
	}
}

func TestCanonicalHashStable(t *testing.T) {
	b := mustJSON(t, validWire())
	var first string
	for i := 0; i < 20; i++ {
		c, err := LoadCatalog(b)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if i == 0 {
			first = c.Hash
			continue
		}
		if c.Hash != first {
			t.Fatalf("hash changed across loads: %q vs %q", first, c.Hash)
		}
	}
}

func TestCanonicalHashContentSensitive(t *testing.T) {
	a, _ := LoadCatalog(mustJSON(t, validWire()))
	w := validWire()
	w.Rules[0].InputPrice = "0.6"
	b, _ := LoadCatalog(mustJSON(t, w))
	if a.Hash == b.Hash {
		t.Fatal("hash must change when content changes")
	}
}

func TestResolveExact(t *testing.T) {
	c, err := LoadCatalog(mustJSON(t, validWire()))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)

	r, notYet, expired := c.resolveExact("Qwen/Qwen3-14B", types.ModelOperationChat, now)
	if r.ruleID == "" || notYet || expired {
		t.Fatalf("expected active exact rule, got %+v notYet=%v expired=%v", r, notYet, expired)
	}

	// Prefix / case alias must not match.
	if r, _, _ := c.resolveExact("Qwen/Qwen3-14", types.ModelOperationChat, now); r.ruleID != "" {
		t.Fatal("prefix must not match")
	}
	if r, _, _ := c.resolveExact("qwen/qwen3-14b", types.ModelOperationChat, now); r.ruleID != "" {
		t.Fatal("case alias must not match")
	}
	if r, _, _ := c.resolveExact("Qwen/Qwen3-14B", types.ModelOperationEmbedding, now); r.ruleID != "" {
		t.Fatal("operation mismatch must not match")
	}
}

func TestResolveExactTimeClassification(t *testing.T) {
	c, _ := LoadCatalog(mustJSON(t, validWire()))
	// Before valid_from.
	if r, notYet, expired := c.resolveExact("Qwen/Qwen3-14B", types.ModelOperationChat, time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)); r.ruleID != "" || !notYet || expired {
		t.Fatalf("expected not-yet-valid, got rule=%q notYet=%v expired=%v", r.ruleID, notYet, expired)
	}
	// After valid_to.
	if r, notYet, expired := c.resolveExact("Qwen/Qwen3-14B", types.ModelOperationChat, time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)); r.ruleID != "" || notYet || !expired {
		t.Fatalf("expected expired, got rule=%q notYet=%v expired=%v", r.ruleID, notYet, expired)
	}
}

func TestParseDecimalNanos(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"0", 0, false},
		{"0.5", 500_000_000, false},
		{"2", 2_000_000_000, false},
		{"1.26", 1_260_000_000, false},
		{"0.0000000005", 1, false},
		{"0.0000000004", 0, false},
		{"-1", 0, true},
		{"abc", 0, true},
		{"1.2.3", 0, true},
	}
	for _, c := range cases {
		got, err := parseDecimalNanos(c.in)
		if c.err {
			if err == nil {
				t.Errorf("parseDecimalNanos(%q): expected error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDecimalNanos(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseDecimalNanos(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
