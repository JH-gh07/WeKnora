package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func item(id string, s types.EvaluationItemStatus) *types.EvaluationRunItem {
	return &types.EvaluationRunItem{ItemID: id, Status: s}
}

func TestAggregateRunItemsCompleted(t *testing.T) {
	items := []*types.EvaluationRunItem{
		item("1", types.EvaluationItemStatusSucceeded),
		item("2", types.EvaluationItemStatusSucceeded),
		item("3", types.EvaluationItemStatusSucceeded),
	}
	a, err := AggregateRunItems(items, true)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if a.LegacyOnly || a.Total != 3 || a.Terminal != 3 || a.Succeeded != 3 || a.FailedTerm != 0 {
		t.Fatalf("aggregate = %+v", a)
	}
	if a.Status != types.EvaluationRunStatusCompleted {
		t.Fatalf("status = %s, want COMPLETED", a.Status)
	}
	if !a.Conserved() {
		t.Fatalf("ledger not conserved: %+v", a)
	}
}

func TestAggregateRunItemsPartial(t *testing.T) {
	items := []*types.EvaluationRunItem{
		item("1", types.EvaluationItemStatusSucceeded),
		item("2", types.EvaluationItemStatusFailedTerm),
		item("3", types.EvaluationItemStatusSucceeded),
	}
	a, err := AggregateRunItems(items, true)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if a.Total != 3 || a.Terminal != 3 || a.FailedTerm != 1 {
		t.Fatalf("aggregate = %+v", a)
	}
	if a.Status != types.EvaluationRunStatusPartial {
		t.Fatalf("status = %s, want PARTIAL", a.Status)
	}
}

func TestAggregateRunItemsRunning(t *testing.T) {
	items := []*types.EvaluationRunItem{
		item("1", types.EvaluationItemStatusSucceeded),
		item("2", types.EvaluationItemStatusRunning),
		item("3", types.EvaluationItemStatusPending),
		item("4", types.EvaluationItemStatusRetryWait),
		item("5", types.EvaluationItemStatusReclaimable),
	}
	a, err := AggregateRunItems(items, true)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if a.Total != 5 || a.Terminal != 1 || a.Succeeded != 1 {
		t.Fatalf("aggregate = %+v", a)
	}
	if a.Status != types.EvaluationRunStatusRunning {
		t.Fatalf("status = %s, want RUNNING", a.Status)
	}
	if a.Pending != 1 || a.Running != 1 || a.RetryWait != 1 || a.Reclaim != 1 {
		t.Fatalf("bucket counts wrong: %+v", a)
	}
	if !a.Conserved() {
		t.Fatalf("ledger not conserved: %+v", a)
	}
}

func TestAggregateRunItemsLegacyOnly(t *testing.T) {
	a, err := AggregateRunItems(nil, false)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if !a.LegacyOnly {
		t.Fatalf("empty items must be LEGACY_AGGREGATE_ONLY: %+v", a)
	}
	if a.Total != 0 || a.Terminal != 0 {
		t.Fatalf("legacy aggregate must be empty: %+v", a)
	}
}

func TestAggregateRunItemsCanonicalHashStable(t *testing.T) {
	items := []*types.EvaluationRunItem{
		{TenantID: 1, RunID: "r", ItemID: "2", Status: types.EvaluationItemStatusSucceeded, ResultArtifactHash: "h2", ResultJSON: types.JSON(`{"p":0.5}`)},
		{TenantID: 1, RunID: "r", ItemID: "1", Status: types.EvaluationItemStatusSucceeded, ResultArtifactHash: "h1", ResultJSON: types.JSON(`{"p":0.1}`)},
	}
	a, err := AggregateRunItems(items, true)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	first, err := a.CanonicalHash(items)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	// Checkpoint B: three consecutive recomputations must yield identical hashes,
	// and the hash must be order-independent (sorted by item_id).
	for i := 0; i < 3; i++ {
		recomputed, err := AggregateRunItems(items, true)
		if err != nil {
			t.Fatalf("recompute %d: %v", i, err)
		}
		if got, err := recomputed.CanonicalHash(items); err != nil || got != first {
			t.Fatalf("recompute %d hash drifted: %s vs %s", i, got, first)
		}
	}
	reordered := []*types.EvaluationRunItem{items[1], items[0]}
	reorderedAggregate, err := AggregateRunItems(reordered, true)
	if err != nil {
		t.Fatalf("reordered aggregate: %v", err)
	}
	if got, err := reorderedAggregate.CanonicalHash(reordered); err != nil || got != first {
		t.Fatalf("order-dependent canonical hash: %s vs %s", got, first)
	}
	// A changed fact changes the hash.
	changed := []*types.EvaluationRunItem{
		{TenantID: 1, RunID: "r", ItemID: "2", Status: types.EvaluationItemStatusSucceeded, ResultArtifactHash: "h2", ResultJSON: types.JSON(`{"p":0.5}`)},
		{TenantID: 1, RunID: "r", ItemID: "1", Status: types.EvaluationItemStatusFailedTerm, ResultArtifactHash: "h1", ResultJSON: types.JSON(`{"p":0.1}`)},
	}
	changedAggregate, err := AggregateRunItems(changed, true)
	if err != nil {
		t.Fatalf("changed aggregate: %v", err)
	}
	if got, err := changedAggregate.CanonicalHash(changed); err != nil || got == first {
		t.Fatalf("canonical hash did not change on a changed fact")
	}
}

// TestAggregateRunItemsConservationAcrossBuckets exercises every status bucket to
// prove I09 (total = sum of every state) holds for a mixed ledger.
func TestAggregateRunItemsConservationAcrossBuckets(t *testing.T) {
	items := []*types.EvaluationRunItem{
		item("1", types.EvaluationItemStatusPending),
		item("2", types.EvaluationItemStatusRunning),
		item("3", types.EvaluationItemStatusRetryWait),
		item("4", types.EvaluationItemStatusReclaimable),
		item("5", types.EvaluationItemStatusSucceeded),
		item("6", types.EvaluationItemStatusFailedTerm),
		item("7", types.EvaluationItemStatusCancelled),
	}
	a, err := AggregateRunItems(items, true)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if a.Total != 7 || a.Terminal != 3 {
		t.Fatalf("aggregate = %+v", a)
	}
	if !a.Conserved() {
		t.Fatalf("I09 violated: %+v", a)
	}
}

func TestAggregateRunItemsRejectsInvalidFactsWithoutPanic(t *testing.T) {
	cases := map[string][]*types.EvaluationRunItem{
		"nil item":      {nil},
		"unknown state": {item("1", types.EvaluationItemStatus("BROKEN"))},
		"duplicate id":  {item("1", types.EvaluationItemStatusSucceeded), item("1", types.EvaluationItemStatusSucceeded)},
		"mixed runs": {
			{TenantID: 1, RunID: "r1", ItemID: "1", Status: types.EvaluationItemStatusSucceeded},
			{TenantID: 1, RunID: "r2", ItemID: "2", Status: types.EvaluationItemStatusSucceeded},
		},
	}
	for name, items := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := AggregateRunItems(items, true); err == nil {
				t.Fatal("AggregateRunItems() error = nil, want invalid-ledger error")
			}
		})
	}
}

func TestAggregateRunItemsCanonicalizesJSONAndTreatsCancellationAsPartial(t *testing.T) {
	aItems := []*types.EvaluationRunItem{{TenantID: 1, RunID: "r", ItemID: "1", Status: types.EvaluationItemStatusSucceeded, ResultJSON: types.JSON(`{"a":1,"b":2}`)}}
	bItems := []*types.EvaluationRunItem{{TenantID: 1, RunID: "r", ItemID: "1", Status: types.EvaluationItemStatusSucceeded, ResultJSON: types.JSON(`{ "b": 2, "a": 1 }`)}}
	a, err := AggregateRunItems(aItems, true)
	if err != nil {
		t.Fatalf("aggregate a: %v", err)
	}
	b, err := AggregateRunItems(bItems, true)
	if err != nil {
		t.Fatalf("aggregate b: %v", err)
	}
	ah, err := a.CanonicalHash(aItems)
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	bh, err := b.CanonicalHash(bItems)
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if ah != bh {
		t.Fatalf("semantic JSON changed hash: %s != %s", ah, bh)
	}

	cancelled, err := AggregateRunItems([]*types.EvaluationRunItem{item("1", types.EvaluationItemStatusCancelled)}, true)
	if err != nil {
		t.Fatalf("cancelled aggregate: %v", err)
	}
	if cancelled.Status != types.EvaluationRunStatusPartial {
		t.Fatalf("cancelled-only status = %s, want PARTIAL", cancelled.Status)
	}
}

func TestAggregateRunItemsDistinguishesLegacyFromZeroItemLedger(t *testing.T) {
	legacy, err := AggregateRunItems(nil, false)
	if err != nil {
		t.Fatalf("legacy: %v", err)
	}
	empty, err := AggregateRunItems([]*types.EvaluationRunItem{}, true)
	if err != nil {
		t.Fatalf("empty ledger: %v", err)
	}
	if !legacy.LegacyOnly || empty.LegacyOnly || empty.Status != types.EvaluationRunStatusCompleted {
		t.Fatalf("legacy=%+v empty=%+v", legacy, empty)
	}
}
