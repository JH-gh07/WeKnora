package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func testProtocolParams(topK int) *types.ChatManage {
	return &types.ChatManage{
		PipelineRequest: types.PipelineRequest{
			VectorThreshold:  0.5,
			KeywordThreshold: 0.3,
			EmbeddingTopK:    10,
			MaxRounds:        3,
			RerankTopK:       5,
			RerankThreshold:  0.4,
			RerankModelID:    "rerank-1",
			ChatModelID:      "chat-1",
			SummaryConfig: types.SummaryConfig{
				MaxTokens:           512,
				TopK:                topK,
				TopP:                0.9,
				Temperature:         0.7,
				Prompt:              "summarize this context",
				ContextTemplate:     "context: {{.context}}",
				NoMatchPrefix:       "no match found",
				MaxCompletionTokens: 256,
			},
			FallbackStrategy:     types.FallbackStrategyFixed,
			FallbackResponse:     "I don't know",
			FallbackPrompt:       "fallback prompt text",
			EnableRewrite:        true,
			EnableQueryExpansion: false,
			RewritePromptSystem:  "rewrite system prompt",
			RewritePromptUser:    "rewrite user prompt",
		},
	}
}

func testProtocolInput(topK int) protocolSnapshotInput {
	return protocolSnapshotInput{
		DatasetID:          "default",
		DatasetContentHash: "dataset-content-sha256",
		EmbeddingModelID:   "emb-1",
		ChatModelID:        "chat-1",
		RerankModelID:      "rerank-1",
		Params:             testProtocolParams(topK),
	}
}

func TestProtocolHashStableForEquivalentProtocol(t *testing.T) {
	// T3: byte/field-equivalent protocol snapshots must produce the same hash.
	in := testProtocolInput(10)
	_, h1, err := buildProtocolSnapshot(in)
	if err != nil {
		t.Fatalf("buildProtocolSnapshot: %v", err)
	}
	_, h2, err := buildProtocolSnapshot(in)
	if err != nil {
		t.Fatalf("buildProtocolSnapshot: %v", err)
	}
	if h1 == "" || h1 != h2 {
		t.Fatalf("equivalent protocol must produce equal non-empty hash, got %q vs %q", h1, h2)
	}
}

func TestProtocolHashSensitiveToTopK(t *testing.T) {
	// T4: top_k 10 -> 20 must change the hash.
	_, h1, err := buildProtocolSnapshot(testProtocolInput(10))
	if err != nil {
		t.Fatalf("buildProtocolSnapshot: %v", err)
	}
	_, h2, err := buildProtocolSnapshot(testProtocolInput(20))
	if err != nil {
		t.Fatalf("buildProtocolSnapshot: %v", err)
	}
	if h1 == h2 {
		t.Fatalf("top_k change must change protocol_hash, got identical %q", h1)
	}
}

func TestProtocolHashInsensitiveToGitCommit(t *testing.T) {
	// T5: protocol unchanged, commit AAA -> BBB must keep protocol_hash equal
	// and only change provenance.git_commit. The protocol builder takes no
	// commit, so this asserts the boundary at the builder level.
	_, h1, err := buildProtocolSnapshot(testProtocolInput(10))
	if err != nil {
		t.Fatalf("buildProtocolSnapshot: %v", err)
	}
	_, h2, err := buildProtocolSnapshot(testProtocolInput(10))
	if err != nil {
		t.Fatalf("buildProtocolSnapshot: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("commit change must NOT change protocol_hash, got %q vs %q", h1, h2)
	}

	p1, err := buildRunProvenance(runProvenance{
		SchemaVersion: evaluationProtocolSchemaVersion,
		GitCommit:     "AAA",
		AppVersion:    "1.0.0",
		DBDriver:      "sqlite",
	})
	if err != nil {
		t.Fatalf("buildRunProvenance: %v", err)
	}
	p2, err := buildRunProvenance(runProvenance{
		SchemaVersion: evaluationProtocolSchemaVersion,
		GitCommit:     "BBB",
		AppVersion:    "1.0.0",
		DBDriver:      "sqlite",
	})
	if err != nil {
		t.Fatalf("buildRunProvenance: %v", err)
	}
	if string(p1) == string(p2) {
		t.Fatalf("provenance must reflect different git commits")
	}
	var v1, v2 runProvenance
	if err := json.Unmarshal(p1, &v1); err != nil {
		t.Fatalf("unmarshal provenance: %v", err)
	}
	if err := json.Unmarshal(p2, &v2); err != nil {
		t.Fatalf("unmarshal provenance: %v", err)
	}
	if v1.GitCommit != "AAA" || v2.GitCommit != "BBB" {
		t.Fatalf("provenance git_commit mismatch: %q %q", v1.GitCommit, v2.GitCommit)
	}
}

func TestProtocolSnapshotIsPromptFree(t *testing.T) {
	snap, _, err := buildProtocolSnapshot(testProtocolInput(10))
	if err != nil {
		t.Fatalf("buildProtocolSnapshot: %v", err)
	}
	s := string(snap)
	// No raw prompt text may be persisted.
	for _, secret := range []string{
		"summarize this context",
		"context: {{.context}}",
		"no match found",
		"I don't know",
		"fallback prompt text",
		"rewrite system prompt",
		"rewrite user prompt",
	} {
		if strings.Contains(s, secret) {
			t.Fatalf("protocol snapshot leaked prompt text: %q", secret)
		}
	}
	// Prompt fields must be represented as digests.
	if !strings.Contains(s, "sha256") || !strings.Contains(s, "length") {
		t.Fatalf("protocol snapshot must contain prompt digests (sha256+length)")
	}
	// Measurement contract is honest.
	if !strings.Contains(s, measurementContractUnversioned) {
		t.Fatalf("protocol snapshot must record measurement_contract_status=UNVERSIONED")
	}
}

func TestDatasetContentHashStableAcrossOrder(t *testing.T) {
	a := []*types.QAPair{
		{QID: 1, Question: "q1", PIDs: []int{0}, Passages: []string{"p0"}, Answer: "a1"},
		{QID: 2, Question: "q2", PIDs: []int{1}, Passages: []string{"p1"}, Answer: "a2"},
	}
	b := []*types.QAPair{
		{QID: 2, Question: "q2", PIDs: []int{1}, Passages: []string{"p1"}, Answer: "a2"},
		{QID: 1, Question: "q1", PIDs: []int{0}, Passages: []string{"p0"}, Answer: "a1"},
	}
	if datasetContentHash(a) != datasetContentHash(b) {
		t.Fatalf("dataset content hash must be order-independent")
	}
}
