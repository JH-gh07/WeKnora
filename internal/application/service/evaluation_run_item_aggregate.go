package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/Tencent/WeKnora/internal/types"
)

// AggregateSourceLegacy marks a run whose durable item facts are absent (a
// pre-Step-6 run). Such a run is read as LEGACY_AGGREGATE_ONLY: its aggregate
// is a historical value, not a recomputation from terminal item facts.
const AggregateSourceLegacy = "LEGACY_AGGREGATE_ONLY"

// RunItemAggregate is the run summary mechanically recomputed from the item
// facts (Task016 Step 6, §3.3). It is a pure function of the terminal items —
// an in-memory accumulator is only ever an acceleration cache, never the source
// of truth (I10).
type RunItemAggregate struct {
	// LegacyOnly is true when no item facts exist (aggregate source is
	// LEGACY_AGGREGATE_ONLY), so a historical aggregate must not be presented as
	// a recomputation.
	LegacyOnly bool
	Total      int
	Terminal   int
	Succeeded  int
	FailedTerm int
	Cancelled  int
	Pending    int
	Running    int
	RetryWait  int
	Reclaim    int
	// Status is the derived run status (COMPLETED / PARTIAL / RUNNING). FAILED is
	// not derivable from item facts alone — it requires a run-level control fact
	// (added in crash-recovery) and is therefore not produced here.
	Status types.EvaluationRunStatus
}

var (
	ErrEvaluationLedgerInvalid    = errors.New("evaluation item ledger is invalid")
	ErrEvaluationLedgerIncomplete = errors.New("evaluation item ledger is not terminal")
)

// AggregateRunItems recomputes the run summary from the item facts. It never
// reads the persisted run row, so a run can always be re-derived from its items
// (Checkpoint B: three consecutive canonical hashes must match).
func AggregateRunItems(items []*types.EvaluationRunItem, ledgerPresent bool) (RunItemAggregate, error) {
	a := RunItemAggregate{Total: len(items)}
	if !ledgerPresent {
		if len(items) != 0 {
			return RunItemAggregate{}, fmt.Errorf("%w: legacy aggregate carries item facts", ErrEvaluationLedgerInvalid)
		}
		a.LegacyOnly = true
		return a, nil
	}
	seen := make(map[string]struct{}, len(items))
	var tenantID uint64
	var runID string
	for _, it := range items {
		if it == nil {
			return RunItemAggregate{}, fmt.Errorf("%w: nil item", ErrEvaluationLedgerInvalid)
		}
		if _, exists := seen[it.ItemID]; exists {
			return RunItemAggregate{}, fmt.Errorf("%w: duplicate item_id %q", ErrEvaluationLedgerInvalid, it.ItemID)
		}
		seen[it.ItemID] = struct{}{}
		if tenantID == 0 && runID == "" {
			tenantID, runID = it.TenantID, it.RunID
		} else if it.TenantID != tenantID || it.RunID != runID {
			return RunItemAggregate{}, fmt.Errorf("%w: mixed tenant/run facts", ErrEvaluationLedgerInvalid)
		}
		switch it.Status {
		case types.EvaluationItemStatusPending:
			a.Pending++
		case types.EvaluationItemStatusRunning:
			a.Running++
		case types.EvaluationItemStatusRetryWait:
			a.RetryWait++
		case types.EvaluationItemStatusReclaimable:
			a.Reclaim++
		case types.EvaluationItemStatusSucceeded:
			a.Succeeded++
		case types.EvaluationItemStatusFailedTerm:
			a.FailedTerm++
		case types.EvaluationItemStatusCancelled:
			a.Cancelled++
		default:
			return RunItemAggregate{}, fmt.Errorf("%w: item %q has status %q", ErrEvaluationLedgerInvalid, it.ItemID, it.Status)
		}
	}
	a.Terminal = a.Succeeded + a.FailedTerm + a.Cancelled

	switch {
	case a.Terminal == a.Total && a.FailedTerm == 0 && a.Cancelled == 0:
		a.Status = types.EvaluationRunStatusCompleted
	case a.Terminal == a.Total:
		a.Status = types.EvaluationRunStatusPartial
	default:
		a.Status = types.EvaluationRunStatusRunning
	}
	if !a.Conserved() {
		return RunItemAggregate{}, fmt.Errorf("%w: state buckets do not conserve total", ErrEvaluationLedgerInvalid)
	}
	return a, nil
}

// Conserved reports the ledger conservation invariant (I09): the total equals
// the sum of every non-terminal and terminal bucket. A violated ledger must be
// surfaced, never silently re-aggregated.
func (a RunItemAggregate) Conserved() bool {
	sum := a.Pending + a.Running + a.RetryWait + a.Reclaim + a.Succeeded + a.FailedTerm + a.Cancelled
	return sum == a.Total
}

// CanonicalHash returns a deterministic SHA-256 over the sorted terminal item
// facts and the derived counts, so re-aggregating the same items always yields
// the identical hash (Checkpoint B: 3x recompute must match).
func (a RunItemAggregate) CanonicalHash(items []*types.EvaluationRunItem) (string, error) {
	if a.LegacyOnly || !a.Conserved() {
		return "", fmt.Errorf("%w: aggregate is legacy or unconserved", ErrEvaluationLedgerInvalid)
	}
	if a.Terminal != a.Total {
		return "", ErrEvaluationLedgerIncomplete
	}
	sorted := append([]*types.EvaluationRunItem(nil), items...)
	slices.SortStableFunc(sorted, func(i, j *types.EvaluationRunItem) int {
		if i == nil || j == nil {
			if i == j {
				return 0
			}
			if i == nil {
				return -1
			}
			return 1
		}
		if i.ItemID < j.ItemID {
			return -1
		}
		if i.ItemID > j.ItemID {
			return 1
		}
		return 0
	})
	type canonicalItem struct {
		TenantID           uint64                     `json:"tenant_id"`
		RunID              string                     `json:"run_id"`
		ItemID             string                     `json:"item_id"`
		Status             types.EvaluationItemStatus `json:"status"`
		TerminalReason     string                     `json:"terminal_reason"`
		ResultArtifactHash string                     `json:"result_artifact_hash"`
		ResultJSON         json.RawMessage            `json:"result_json,omitempty"`
	}
	type canonicalAggregate struct {
		Status     types.EvaluationRunStatus `json:"status"`
		Total      int                       `json:"total"`
		Succeeded  int                       `json:"succeeded"`
		FailedTerm int                       `json:"failed_terminal"`
		Cancelled  int                       `json:"cancelled"`
		Items      []canonicalItem           `json:"items"`
	}
	doc := canonicalAggregate{Status: a.Status, Total: a.Total, Succeeded: a.Succeeded, FailedTerm: a.FailedTerm, Cancelled: a.Cancelled, Items: make([]canonicalItem, 0, len(sorted))}
	for _, it := range sorted {
		if it == nil || !it.Status.IsTerminal() {
			return "", ErrEvaluationLedgerIncomplete
		}
		canonicalResult, err := canonicalJSON(it.ResultJSON)
		if err != nil {
			return "", fmt.Errorf("%w: item %q result_json: %v", ErrEvaluationLedgerInvalid, it.ItemID, err)
		}
		doc.Items = append(doc.Items, canonicalItem{TenantID: it.TenantID, RunID: it.RunID, ItemID: it.ItemID, Status: it.Status, TerminalReason: it.TerminalReason, ResultArtifactHash: it.ResultArtifactHash, ResultJSON: canonicalResult})
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("canonical aggregate: %w", err)
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}

// canonicalJSON normalizes object key order, whitespace and number rendering
// before a result enters a cross-database identity. PostgreSQL JSONB and SQLite
// TEXT may otherwise return different bytes for the same JSON value.
func canonicalJSON(raw []byte) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return json.Marshal(value)
}
