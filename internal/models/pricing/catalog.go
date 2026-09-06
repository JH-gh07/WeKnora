// Package pricing implements the one-provider pricing writer for Task012.
//
// It combines provider-reported usage with a frozen, immutable, in-memory
// pricing catalog to produce a reproducible historical estimated-cost fact.
// The catalog is compiled into the binary (go:embed); it never performs a
// network lookup and never reads the current model configuration. Rules are
// matched by exact provider + exact model + exact operation only, with no
// alias/prefix/case fallback (Task012 §5.3, §10.4).
package pricing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	// SchemaVersionV1 is the only accepted catalog schema version.
	SchemaVersionV1 = "pricing_catalog/1"
	// BillingUnitPer1MTokens is the only billing unit accepted by Task012.
	BillingUnitPer1MTokens = "per_1m_tokens"
)

var (
	decimalPriceRe = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	currencyRe     = regexp.MustCompile(`^[A-Z]{3}$`)

	// officialHosts maps a catalog provider to its HTTPS official pricing hosts.
	// A source_url is accepted only when it is HTTPS and its host is one of the
	// listed hosts or a direct subdomain of one of them.
	officialHosts = map[string][]string{
		"siliconflow": {"siliconflow.cn"},
	}
)

// LoadedCatalog is an immutable, validated pricing catalog ready for exact rule
// resolution. It is safe for concurrent reads.
type LoadedCatalog struct {
	CatalogID        string
	CatalogVersion   string
	Provider         string
	Currency         string
	SourceURL        string
	SourceCapturedAt time.Time
	ReviewAfter      time.Time
	// Hash is the hex SHA-256 of the canonical bytes.
	Hash string
	// Canonical is the canonical JSON used to compute Hash.
	Canonical []byte

	rules []loadedRule
}

type loadedRule struct {
	ruleID          string
	modelExact      string
	operation       string
	billingUnit     string
	validFrom       time.Time
	validTo         time.Time
	inputNanos      int64
	outputNanos     int64
	cacheReadNanos  *int64
	cacheWriteNanos *int64
}

// catalogWire mirrors the on-disk JSON. json.Decoder.DisallowUnknownFields
// rejects any top-level or rule field that is not declared here, so a typo can
// never be silently ignored (Task012 §5.3).
type catalogWire struct {
	SchemaVersion    string     `json:"schema_version"`
	CatalogID        string     `json:"catalog_id"`
	CatalogVersion   string     `json:"catalog_version"`
	Provider         string     `json:"provider"`
	Currency         string     `json:"currency"`
	SourceURL        string     `json:"source_url"`
	SourceCapturedAt string     `json:"source_captured_at"`
	ReviewAfter      string     `json:"review_after"`
	Rules            []ruleWire `json:"rules"`
}

type ruleWire struct {
	RuleID          string  `json:"rule_id"`
	ModelExact      string  `json:"model_exact"`
	Operation       string  `json:"operation"`
	ValidFrom       string  `json:"valid_from"`
	ValidTo         string  `json:"valid_to"`
	BillingUnit     string  `json:"billing_unit"`
	InputPrice      string  `json:"input_price"`
	OutputPrice     string  `json:"output_price"`
	CacheReadPrice  *string `json:"cache_read_price"`
	CacheWritePrice *string `json:"cache_write_price"`
}

// LoadCatalog parses and validates a catalog document, returning an immutable
// LoadedCatalog. It rejects unknown fields, malformed decimals, invalid
// currency, non-official sources, overlapping intervals, and other violations
// described in Task012 §5.3.
func LoadCatalog(data []byte) (*LoadedCatalog, error) {
	var w catalogWire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("pricing catalog: decode: %w", err)
	}
	if w.SchemaVersion != SchemaVersionV1 {
		return nil, fmt.Errorf("pricing catalog: unsupported schema_version %q", w.SchemaVersion)
	}
	if strings.TrimSpace(w.CatalogID) == "" {
		return nil, errors.New("pricing catalog: catalog_id is required")
	}
	if strings.TrimSpace(w.CatalogVersion) == "" {
		return nil, errors.New("pricing catalog: catalog_version is required")
	}
	if strings.TrimSpace(w.Provider) == "" {
		return nil, errors.New("pricing catalog: provider is required")
	}
	if !currencyRe.MatchString(w.Currency) {
		return nil, fmt.Errorf("pricing catalog: invalid currency %q (want ISO 4217 uppercase)", w.Currency)
	}
	if err := validateSourceURL(w.Provider, w.SourceURL); err != nil {
		return nil, err
	}
	capturedAt, err := parseTime("source_captured_at", w.SourceCapturedAt)
	if err != nil {
		return nil, err
	}
	reviewAfter, err := parseTime("review_after", w.ReviewAfter)
	if err != nil {
		return nil, err
	}
	if !reviewAfter.After(capturedAt) {
		return nil, errors.New("pricing catalog: review_after must be after source_captured_at")
	}
	if len(w.Rules) == 0 {
		return nil, errors.New("pricing catalog: at least one rule is required")
	}

	lc := &LoadedCatalog{
		CatalogID:        w.CatalogID,
		CatalogVersion:   w.CatalogVersion,
		Provider:         w.Provider,
		Currency:         w.Currency,
		SourceURL:        w.SourceURL,
		SourceCapturedAt: capturedAt,
		ReviewAfter:      reviewAfter,
	}
	for i, rw := range w.Rules {
		r, err := parseRule(rw, capturedAt, i)
		if err != nil {
			return nil, err
		}
		lc.rules = append(lc.rules, r)
	}
	if err := validateNoOverlap(lc.rules); err != nil {
		return nil, err
	}

	// Canonical hash is computed from the re-marshalled wire struct (stable key
	// order, normalized whitespace) so the digest depends only on content.
	canonical, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("pricing catalog: canonical marshal: %w", err)
	}
	lc.Canonical = canonical
	sum := sha256.Sum256(canonical)
	lc.Hash = hex.EncodeToString(sum[:])
	return lc, nil
}

func parseRule(rw ruleWire, capturedAt time.Time, index int) (loadedRule, error) {
	prefix := fmt.Sprintf("pricing catalog: rules[%d]", index)
	if strings.TrimSpace(rw.RuleID) == "" {
		return loadedRule{}, fmt.Errorf("%s: rule_id is required", prefix)
	}
	if strings.TrimSpace(rw.ModelExact) == "" {
		return loadedRule{}, fmt.Errorf("%s: model_exact is required", prefix)
	}
	if strings.TrimSpace(rw.Operation) == "" {
		return loadedRule{}, fmt.Errorf("%s: operation is required", prefix)
	}
	if rw.BillingUnit != BillingUnitPer1MTokens {
		return loadedRule{}, fmt.Errorf("%s: unsupported billing_unit %q", prefix, rw.BillingUnit)
	}
	validFrom, err := parseTime(prefix+".valid_from", rw.ValidFrom)
	if err != nil {
		return loadedRule{}, err
	}
	validTo, err := parseTime(prefix+".valid_to", rw.ValidTo)
	if err != nil {
		return loadedRule{}, err
	}
	if !validTo.After(validFrom) {
		return loadedRule{}, fmt.Errorf("%s: valid_from must be before valid_to", prefix)
	}
	if capturedAt.After(validFrom) {
		return loadedRule{}, fmt.Errorf("%s: source_captured_at must not be after valid_from", prefix)
	}
	inputNanos, err := parseDecimalNanos(rw.InputPrice)
	if err != nil {
		return loadedRule{}, fmt.Errorf("%s: input_price: %w", prefix, err)
	}
	outputNanos, err := parseDecimalNanos(rw.OutputPrice)
	if err != nil {
		return loadedRule{}, fmt.Errorf("%s: output_price: %w", prefix, err)
	}
	r := loadedRule{
		ruleID:      rw.RuleID,
		modelExact:  rw.ModelExact,
		operation:   rw.Operation,
		billingUnit: rw.BillingUnit,
		validFrom:   validFrom,
		validTo:     validTo,
		inputNanos:  inputNanos,
		outputNanos: outputNanos,
	}
	if rw.CacheReadPrice != nil {
		n, err := parseDecimalNanos(*rw.CacheReadPrice)
		if err != nil {
			return loadedRule{}, fmt.Errorf("%s: cache_read_price: %w", prefix, err)
		}
		r.cacheReadNanos = &n
	}
	if rw.CacheWritePrice != nil {
		n, err := parseDecimalNanos(*rw.CacheWritePrice)
		if err != nil {
			return loadedRule{}, fmt.Errorf("%s: cache_write_price: %w", prefix, err)
		}
		r.cacheWriteNanos = &n
	}
	return r, nil
}

func validateNoOverlap(rules []loadedRule) error {
	// Group by identity (model_exact + operation) and reject any overlapping
	// effective interval within the same identity.
	type identity struct{ model, op string }
	byIdentity := map[identity][]loadedRule{}
	for _, r := range rules {
		k := identity{r.modelExact, r.operation}
		byIdentity[k] = append(byIdentity[k], r)
	}
	for k, list := range byIdentity {
		sorted := append([]loadedRule(nil), list...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].validFrom.Before(sorted[j].validFrom) })
		for i := 1; i < len(sorted); i++ {
			prev, cur := sorted[i-1], sorted[i]
			if !cur.validFrom.Before(prev.validTo) {
				continue
			}
			return fmt.Errorf("pricing catalog: overlapping intervals for %s/%s (%s and %s)",
				k.model, k.op, prev.ruleID, cur.ruleID)
		}
	}
	return nil
}

func validateSourceURL(provider, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("pricing catalog: invalid source_url: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("pricing catalog: source_url must be HTTPS, got %q", raw)
	}
	hosts, ok := officialHosts[provider]
	if !ok {
		return fmt.Errorf("pricing catalog: no official host allowlist for provider %q", provider)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("pricing catalog: source_url has empty host")
	}
	for _, h := range hosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return nil
		}
	}
	return fmt.Errorf("pricing catalog: source_url host %q is not an official %s domain", host, provider)
}

func parseTime(field, s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("pricing catalog: %s: invalid RFC3339 time %q: %w", field, s, err)
	}
	return t, nil
}

// parseDecimalNanos parses a non-negative finite decimal string into an integer
// number of nanos (1e-9 currency units), rounding half-up to the nearest nano.
// It uses math/big.Rat and never binary float for the conversion.
func parseDecimalNanos(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if !decimalPriceRe.MatchString(s) {
		return 0, fmt.Errorf("invalid decimal price %q", s)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() < 0 {
		return 0, fmt.Errorf("invalid decimal price %q", s)
	}
	// nanos = price * 1e9, round half-up to an integer.
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt64(1_000_000_000))
	scaled = scaled.Add(scaled, new(big.Rat).SetFrac64(1, 2))
	// Floor for non-negative value == round half up.
	q := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	if !q.IsInt64() {
		return 0, fmt.Errorf("price overflow %q", s)
	}
	return q.Int64(), nil
}

// resolveExact returns the active rule for the exact provider/model/operation,
// along with whether the only identity match is not-yet-valid or expired. It
// never does alias/prefix/case fallback.
func (c *LoadedCatalog) resolveExact(modelName string, operation types.ModelOperation, now time.Time) (loadedRule, bool, bool) {
	var matches []loadedRule
	for _, r := range c.rules {
		if r.modelExact == modelName && r.operation == string(operation) {
			matches = append(matches, r)
		}
	}
	if len(matches) == 0 {
		return loadedRule{}, false, false
	}
	for _, r := range matches {
		if !now.Before(r.validFrom) && now.Before(r.validTo) {
			return r, false, false
		}
	}
	// No active interval: classify as future or expired.
	for _, r := range matches {
		if now.Before(r.validFrom) {
			return loadedRule{}, true, false
		}
	}
	return loadedRule{}, false, true
}
