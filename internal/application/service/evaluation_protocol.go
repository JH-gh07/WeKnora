package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// evaluationProtocolSchemaVersion pins the shape of the protocol snapshot so
// a future schema change cannot silently collide with an old hash.
const evaluationProtocolSchemaVersion = "evaluation_protocol/1"

// measurementContractUnversioned is the honest value recorded while the
// evaluator/metric artifact hash is not yet produced by any Task. It must NOT
// be faked into a hash; a later Task replaces it with a real contract version.
const measurementContractUnversioned = "UNVERSIONED"

// EvaluationBuildInfo carries the compile-time identity used by provenance.
// It is injected from the DI container (which reads handler.Version / handler
// .CommitID — the ldflags targets) so the service package never imports the
// handler package (that would create an import cycle).
type EvaluationBuildInfo struct {
	GitCommit  string
	AppVersion string
	GoVersion  string
	BuildTime  string
}

// promptDigest is a secret-free, prompt-free reference to a prompt string.
// Only the digest and length are stored; the original text is never persisted
// into a protocol/provenance snapshot.
type promptDigest struct {
	SHA256 string `json:"sha256"`
	Length int    `json:"length"`
}

// digestPrompt returns a stable digest reference for a prompt string.
func digestPrompt(s string) promptDigest {
	sum := sha256.Sum256([]byte(s))
	return promptDigest{SHA256: hex.EncodeToString(sum[:]), Length: len(s)}
}

// evaluationProtocol is the comparable, secret-free snapshot. Field order is
// the declaration order, which encoding/json preserves, so the serialization
// is deterministic and independent of map iteration order.
type evaluationProtocol struct {
	SchemaVersion             string        `json:"schema_version"`
	DatasetID                 string        `json:"dataset_id"`
	DatasetContentHash        string        `json:"dataset_content_hash"`
	EmbeddingModelID          string        `json:"embedding_model_id"`
	ChatModelID               string        `json:"chat_model_id"`
	RerankModelID             string        `json:"rerank_model_id"`
	SourceKnowledgeBaseID     string        `json:"source_knowledge_base_id"`
	VectorThreshold           float64       `json:"vector_threshold"`
	KeywordThreshold          float64       `json:"keyword_threshold"`
	EmbeddingTopK             int           `json:"embedding_top_k"`
	MaxRounds                 int           `json:"max_rounds"`
	RerankTopK                int           `json:"rerank_top_k"`
	RerankThreshold           float64       `json:"rerank_threshold"`
	Summary                   summaryConfig `json:"summary"`
	FallbackStrategy          string        `json:"fallback_strategy"`
	FallbackResponseDigest    promptDigest  `json:"fallback_response_digest"`
	FallbackPromptDigest      promptDigest  `json:"fallback_prompt_digest"`
	EnableRewrite             bool          `json:"enable_rewrite"`
	EnableQueryExpansion      bool          `json:"enable_query_expansion"`
	RewritePromptSystemDigest promptDigest  `json:"rewrite_prompt_system_digest"`
	RewritePromptUserDigest   promptDigest  `json:"rewrite_prompt_user_digest"`
	MeasurementContractStatus string        `json:"measurement_contract_status"`
}

// summaryConfig mirrors the comparable (non-secret) summary parameters. Prompt
// text is represented only by a digest.
type summaryConfig struct {
	MaxTokens            int         `json:"max_tokens"`
	RepeatPenalty        float64     `json:"repeat_penalty"`
	TopK                 int         `json:"top_k"`
	TopP                 float64     `json:"top_p"`
	FrequencyPenalty     float64     `json:"frequency_penalty"`
	PresencePenalty      float64     `json:"presence_penalty"`
	Temperature          float64     `json:"temperature"`
	Seed                 int         `json:"seed"`
	MaxCompletionTokens  int         `json:"max_completion_tokens"`
	PromptDigest         promptDigest `json:"prompt_digest"`
	ContextTemplateDigest promptDigest `json:"context_template_digest"`
	NoMatchPrefixDigest  promptDigest `json:"no_match_prefix_digest"`
}

// protocolSnapshotInput is the immutable set of facts used to build a protocol
// snapshot. It is decoupled from EvaluationDetail so the builder is unit
// testable without a live pipeline.
type protocolSnapshotInput struct {
	DatasetID             string
	DatasetContentHash    string
	EmbeddingModelID      string
	ChatModelID           string
	RerankModelID         string
	SourceKnowledgeBaseID string
	Params                *types.ChatManage
}

// buildProtocolSnapshot canonicalizes the comparable protocol and derives its
// sha256 identity. It never includes git commit or runtime environment (those
// are provenance), and it never stores prompt text or secrets.
func buildProtocolSnapshot(in protocolSnapshotInput) (types.JSON, string, error) {
	p := &in.Params.PipelineRequest
	proto := evaluationProtocol{
		SchemaVersion:             evaluationProtocolSchemaVersion,
		DatasetID:                 in.DatasetID,
		DatasetContentHash:        in.DatasetContentHash,
		EmbeddingModelID:          in.EmbeddingModelID,
		ChatModelID:               in.ChatModelID,
		RerankModelID:             in.RerankModelID,
		SourceKnowledgeBaseID:     in.SourceKnowledgeBaseID,
		VectorThreshold:           p.VectorThreshold,
		KeywordThreshold:          p.KeywordThreshold,
		EmbeddingTopK:             p.EmbeddingTopK,
		MaxRounds:                 p.MaxRounds,
		RerankTopK:                p.RerankTopK,
		RerankThreshold:           p.RerankThreshold,
		Summary: summaryConfig{
			MaxTokens:            p.SummaryConfig.MaxTokens,
			RepeatPenalty:        p.SummaryConfig.RepeatPenalty,
			TopK:                 p.SummaryConfig.TopK,
			TopP:                 p.SummaryConfig.TopP,
			FrequencyPenalty:     p.SummaryConfig.FrequencyPenalty,
			PresencePenalty:      p.SummaryConfig.PresencePenalty,
			Temperature:          p.SummaryConfig.Temperature,
			Seed:                 p.SummaryConfig.Seed,
			MaxCompletionTokens:  p.SummaryConfig.MaxCompletionTokens,
			PromptDigest:         digestPrompt(p.SummaryConfig.Prompt),
			ContextTemplateDigest: digestPrompt(p.SummaryConfig.ContextTemplate),
			NoMatchPrefixDigest:  digestPrompt(p.SummaryConfig.NoMatchPrefix),
		},
		FallbackStrategy:          string(p.FallbackStrategy),
		FallbackResponseDigest:    digestPrompt(p.FallbackResponse),
		FallbackPromptDigest:      digestPrompt(p.FallbackPrompt),
		EnableRewrite:             p.EnableRewrite,
		EnableQueryExpansion:      p.EnableQueryExpansion,
		RewritePromptSystemDigest: digestPrompt(p.RewritePromptSystem),
		RewritePromptUserDigest:   digestPrompt(p.RewritePromptUser),
		MeasurementContractStatus: measurementContractUnversioned,
	}

	raw, err := json.Marshal(proto)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(raw)
	return types.JSON(raw), hex.EncodeToString(sum[:]), nil
}

// modelRevision is a secret-free summary of a model identity used in
// provenance. It deliberately excludes Parameters (which contain API keys).
type modelRevision struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Source string `json:"source"`
	Name   string `json:"name"`
}

// runProvenance explains where a result came from. git_commit, app_version,
// go_version, build_time, db_driver and model revisions are provenance, NOT
// comparison identity.
type runProvenance struct {
	SchemaVersion string          `json:"schema_version"`
	GitCommit     string          `json:"git_commit"`
	AppVersion    string          `json:"app_version"`
	GoVersion     string          `json:"go_version"`
	BuildTime     string          `json:"build_time"`
	DBDriver      string          `json:"db_driver"`
	StartedAt     time.Time       `json:"started_at"`
	Models        []modelRevision `json:"models"`
}

// buildRunProvenance canonicalizes the provenance record.
func buildRunProvenance(in runProvenance) (types.JSON, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	return types.JSON(raw), nil
}

// datasetContentHash produces a stable content identity for a QA dataset.
// Order is normalized by QID so an equivalent dataset yields the same hash.
func datasetContentHash(dataset []*types.QAPair) string {
	pairs := make([]*types.QAPair, len(dataset))
	copy(pairs, dataset)
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].QID < pairs[j].QID })
	raw, err := json.Marshal(pairs)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
