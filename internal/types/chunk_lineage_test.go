package types

import (
	"encoding/json"
	"testing"
)

// Task016 Step 3 — chunk lineage identity round-trips through the metadata JSON
// column (plan §4.2 方案 B) without a schema migration.

func TestChunkSourcePassageIDRoundTrip(t *testing.T) {
	c := &Chunk{}
	if err := c.SetSourcePassageID("42"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := c.SourcePassageIDValue(); got != "42" {
		t.Fatalf("value = %q, want 42", got)
	}
}

func TestChunkSourcePassageIDPreservesExistingMetadata(t *testing.T) {
	c := &Chunk{}
	meta := &DocumentChunkMetadata{
		GeneratedQuestions: []GeneratedQuestion{{ID: "q1", Question: "who?"}},
	}
	if err := c.SetDocumentMetadata(meta); err != nil {
		t.Fatalf("set metadata: %v", err)
	}
	if err := c.SetSourcePassageID("7"); err != nil {
		t.Fatalf("set passage id: %v", err)
	}
	got, err := c.DocumentMetadata()
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if got.SourcePassageID != "7" {
		t.Fatalf("SourcePassageID = %q, want 7", got.SourcePassageID)
	}
	if len(got.GeneratedQuestions) != 1 || got.GeneratedQuestions[0].ID != "q1" {
		t.Fatalf("generated questions lost: %+v", got.GeneratedQuestions)
	}
}

func TestChunkSourcePassageIDEmptyWhenUnset(t *testing.T) {
	c := &Chunk{}
	if got := c.SourcePassageIDValue(); got != "" {
		t.Fatalf("value = %q, want empty (LINEAGE_UNAVAILABLE)", got)
	}
	var nilChunk *Chunk
	if got := nilChunk.SourcePassageIDValue(); got != "" {
		t.Fatalf("nil value = %q, want empty", got)
	}
}

func TestDocumentChunkMetadataSourcePassageIDSerializes(t *testing.T) {
	meta := &DocumentChunkMetadata{SourcePassageID: "9"}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"source_passage_id":"9"}` {
		t.Fatalf("json = %s", string(b))
	}
	var back DocumentChunkMetadata
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.SourcePassageID != "9" {
		t.Fatalf("back = %q, want 9", back.SourcePassageID)
	}
}
