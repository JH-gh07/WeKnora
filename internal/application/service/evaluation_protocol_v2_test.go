package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service/metric"
)

// Task016 Step 5 — Protocol v2 identity + comparability tests (W3, AC09, AC11).

// baseProtocolV2Input returns a fully-populated deterministic input so every
// mutation is a single-field perturbation of a known-good snapshot.
func baseProtocolV2Input() protocolV2Input {
	return protocolV2Input{
		DatasetID:            "ds-001",
		DatasetContentHash:   digestV2("dataset-content-bytes"),
		LineageSchemaVersion: "lineage/1",
		ParserVersion:        "parser/2",
		ChunkerContractHash:  digestV2("chunker-contract"),
		IndexBackend:         "paradedb",
		DistanceMetric:       "cosine",
		EmbeddingDimension:   1024,
		VectorThreshold:      0.7,
		KeywordThreshold:     0.5,
		EmbeddingTopK:        20,
		RerankTopK:           5,
		RerankThreshold:      0.6,
		MaxRounds:            3,
		FallbackStrategy:     "fixed",
		EnableRewrite:        true,
		EnableQueryExpansion: false,
		GenerationParams: GenerationParamsV2{
			MaxTokens:           2048,
			RepeatPenalty:       1.0,
			TopK:                40,
			TopP:                0.9,
			FrequencyPenalty:    0.0,
			PresencePenalty:     0.0,
			Temperature:         0.2,
			Seed:                42,
			MaxCompletionTokens: 1024,
		},
		ModelFingerprints: []ModelComputationFingerprint{
			{Role: "embedding", Provider: "openai", ModelID: "text-embedding-3-large", DeploymentRevision: "r1", EndpointDigest: digestV2("https://embed.example.com/v1"), ParamsDigest: digestV2("dim=1024")},
			{Role: "chat", Provider: "openai", ModelID: "gpt-4o", DeploymentRevision: "r2", EndpointDigest: digestV2("https://chat.example.com/v1"), ParamsDigest: digestV2("temp=0.2")},
		},
		WorkerCount:   2,
		Concurrency:   4,
		Timeout:       "30s",
		Retry:         1,
		HardwareClass: "m5.large",
		Warmup:        "none",
		CacheState:    "cold",
		Provenance: RunProvenanceV2{
			GitCommit:          "abcdef1234567890",
			AppVersion:         "1.2.3",
			GoVersion:          "go1.26.0",
			BuildTime:          "2026-09-06T00:00:00Z",
			DBDriver:           "postgres",
			OSArch:             "darwin/amd64",
			ContainerDigest:    "sha256:" + digestV2("container-image"),
			EndpointHostDigest: digestV2("eval.example.com"),
			StartedAt:          "2026-09-06T01:00:00Z",
		},
	}
}

func TestBuildProtocolV2FourIdentityClasses(t *testing.T) {
	p, err := buildProtocolV2(baseProtocolV2Input())
	if err != nil {
		t.Fatalf("buildProtocolV2: %v", err)
	}
	if p.SchemaVersion != evaluationProtocolSchemaVersionV2 {
		t.Fatalf("schema version = %q", p.SchemaVersion)
	}
	for name, h := range map[string]string{
		"quality_protocol_hash":     p.QualityHash,
		"performance_protocol_hash": p.PerformanceHash,
		"run_provenance_hash":       p.ProvenanceHash,
		"measurement_contract_hash": p.MeasurementContractHash,
	} {
		if len(h) != 64 {
			t.Fatalf("%s = %q, want 64 hex chars", name, h)
		}
	}
	// The identities must be separated: quality != provenance, quality != performance.
	if p.QualityHash == p.ProvenanceHash {
		t.Fatalf("quality and provenance hashes must differ")
	}
	if p.QualityHash == p.PerformanceHash {
		t.Fatalf("quality and performance hashes must differ")
	}
}

func TestProtocolV2CanonicalSerializationStable(t *testing.T) {
	// AC09 / W3: canonical serialization must yield identical hashes across 100
	// consecutive builds (deterministic field order, no map iteration leakage).
	first, err := buildProtocolV2(baseProtocolV2Input())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for i := 0; i < 100; i++ {
		p, err := buildProtocolV2(baseProtocolV2Input())
		if err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
		if p.QualityHash != first.QualityHash || p.PerformanceHash != first.PerformanceHash || p.ProvenanceHash != first.ProvenanceHash {
			t.Fatalf("iter %d: non-deterministic hash (q=%s p=%s r=%s)", i, p.QualityHash, p.PerformanceHash, p.ProvenanceHash)
		}
	}
}

func TestProtocolV2FingerprintOrderIsDeterministic(t *testing.T) {
	base := baseProtocolV2Input()
	reordered := base
	// Reverse the fingerprint slice: the quality hash must be unchanged because
	// buildProtocolV2 sorts fingerprints deterministically.
	reordered.ModelFingerprints = []ModelComputationFingerprint{base.ModelFingerprints[1], base.ModelFingerprints[0]}
	a, _ := buildProtocolV2(base)
	b, _ := buildProtocolV2(reordered)
	if a.QualityHash != b.QualityHash {
		t.Fatalf("fingerprint order perturbed the quality hash")
	}
}

// mutationCase is one W3 single-field mutation with its pre-registered class
// (protocol_field_classification.tsv).
type mutationCase struct {
	name  string
	class string // QUALITY | PERFORMANCE | PROVENANCE
	apply func(*protocolV2Input)
}

func protocolV2MutationCases() []mutationCase {
	return []mutationCase{
		// ---- QUALITY: dataset identity ----
		{"dataset_id", "QUALITY", func(in *protocolV2Input) { in.DatasetID = "ds-002" }},
		{"dataset_content_hash", "QUALITY", func(in *protocolV2Input) { in.DatasetContentHash = digestV2("different-content") }},
		// ---- QUALITY: lineage / parser / chunker ----
		{"lineage_schema_version", "QUALITY", func(in *protocolV2Input) { in.LineageSchemaVersion = "lineage/2" }},
		{"parser_version", "QUALITY", func(in *protocolV2Input) { in.ParserVersion = "parser/3" }},
		{"chunker_contract_hash", "QUALITY", func(in *protocolV2Input) { in.ChunkerContractHash = digestV2("chunker-v2") }},
		// ---- QUALITY: index / retrieval ----
		{"index_backend", "QUALITY", func(in *protocolV2Input) { in.IndexBackend = "sqlite" }},
		{"distance_metric", "QUALITY", func(in *protocolV2Input) { in.DistanceMetric = "l2" }},
		{"embedding_dimension", "QUALITY", func(in *protocolV2Input) { in.EmbeddingDimension = 768 }},
		{"vector_threshold", "QUALITY", func(in *protocolV2Input) { in.VectorThreshold = 0.75 }},
		{"keyword_threshold", "QUALITY", func(in *protocolV2Input) { in.KeywordThreshold = 0.55 }},
		{"embedding_top_k", "QUALITY", func(in *protocolV2Input) { in.EmbeddingTopK = 30 }},
		{"rerank_top_k", "QUALITY", func(in *protocolV2Input) { in.RerankTopK = 8 }},
		{"rerank_threshold", "QUALITY", func(in *protocolV2Input) { in.RerankThreshold = 0.65 }},
		{"max_rounds", "QUALITY", func(in *protocolV2Input) { in.MaxRounds = 5 }},
		{"fallback_strategy", "QUALITY", func(in *protocolV2Input) { in.FallbackStrategy = "none" }},
		{"enable_rewrite", "QUALITY", func(in *protocolV2Input) { in.EnableRewrite = false }},
		{"enable_query_expansion", "QUALITY", func(in *protocolV2Input) { in.EnableQueryExpansion = true }},
		// ---- QUALITY: generation params ----
		{"generation_params.max_tokens", "QUALITY", func(in *protocolV2Input) { in.GenerationParams.MaxTokens = 4096 }},
		{"generation_params.repeat_penalty", "QUALITY", func(in *protocolV2Input) { in.GenerationParams.RepeatPenalty = 1.2 }},
		{"generation_params.top_p", "QUALITY", func(in *protocolV2Input) { in.GenerationParams.TopP = 0.8 }},
		{"generation_params.temperature", "QUALITY", func(in *protocolV2Input) { in.GenerationParams.Temperature = 0.7 }},
		{"generation_params.seed", "QUALITY", func(in *protocolV2Input) { in.GenerationParams.Seed = 43 }},
		// ---- QUALITY: model computation fingerprint (KF-06) ----
		{"model_fingerprints.provider", "QUALITY", func(in *protocolV2Input) { in.ModelFingerprints[0].Provider = "anthropic" }},
		{"model_fingerprints.model_id", "QUALITY", func(in *protocolV2Input) { in.ModelFingerprints[0].ModelID = "claude-3-5-sonnet" }},
		{"model_fingerprints.endpoint_digest", "QUALITY", func(in *protocolV2Input) {
			in.ModelFingerprints[0].EndpointDigest = digestV2("https://new.example.com")
		}},
		{"model_fingerprints.params_digest", "QUALITY", func(in *protocolV2Input) { in.ModelFingerprints[0].ParamsDigest = digestV2("dim=768") }},
		{"model_fingerprints.add", "QUALITY", func(in *protocolV2Input) {
			in.ModelFingerprints = append(in.ModelFingerprints, ModelComputationFingerprint{Role: "rerank", Provider: "cohere", ModelID: "rerank-v3", EndpointDigest: digestV2("https://rerank.example.com"), ParamsDigest: digestV2("")})
		}},
		// ---- PERFORMANCE ----
		{"worker_count", "PERFORMANCE", func(in *protocolV2Input) { in.WorkerCount = 4 }},
		{"concurrency", "PERFORMANCE", func(in *protocolV2Input) { in.Concurrency = 8 }},
		{"timeout", "PERFORMANCE", func(in *protocolV2Input) { in.Timeout = "60s" }},
		{"retry", "PERFORMANCE", func(in *protocolV2Input) { in.Retry = 3 }},
		{"hardware_class", "PERFORMANCE", func(in *protocolV2Input) { in.HardwareClass = "c5.xlarge" }},
		{"warmup", "PERFORMANCE", func(in *protocolV2Input) { in.Warmup = "100ms" }},
		{"cache_state", "PERFORMANCE", func(in *protocolV2Input) { in.CacheState = "warm" }},
		{"lease_ttl", "PERFORMANCE", func(in *protocolV2Input) { in.LeaseTTL = "90s" }},
		{"heartbeat_interval", "PERFORMANCE", func(in *protocolV2Input) { in.Heartbeat = "30s" }},
		// ---- PROVENANCE (never comparison identity, I05) ----
		{"provenance.git_commit", "PROVENANCE", func(in *protocolV2Input) { in.Provenance.GitCommit = "deadbeef" }},
		{"provenance.app_version", "PROVENANCE", func(in *protocolV2Input) { in.Provenance.AppVersion = "1.2.4" }},
		{"provenance.go_version", "PROVENANCE", func(in *protocolV2Input) { in.Provenance.GoVersion = "go1.27.0" }},
		{"provenance.build_time", "PROVENANCE", func(in *protocolV2Input) { in.Provenance.BuildTime = "2026-09-07T00:00:00Z" }},
		{"provenance.db_driver", "PROVENANCE", func(in *protocolV2Input) { in.Provenance.DBDriver = "sqlite" }},
		{"provenance.os_arch", "PROVENANCE", func(in *protocolV2Input) { in.Provenance.OSArch = "linux/amd64" }},
		{"provenance.container_digest", "PROVENANCE", func(in *protocolV2Input) { in.Provenance.ContainerDigest = "sha256:" + digestV2("other") }},
		{"provenance.endpoint_host_digest", "PROVENANCE", func(in *protocolV2Input) { in.Provenance.EndpointHostDigest = digestV2("other.example.com") }},
		{"provenance.started_at", "PROVENANCE", func(in *protocolV2Input) { in.Provenance.StartedAt = "2026-09-06T02:00:00Z" }},
	}
}

func TestProtocolV2IdentityMutation(t *testing.T) {
	base, err := buildProtocolV2(baseProtocolV2Input())
	if err != nil {
		t.Fatalf("build base: %v", err)
	}
	cases := protocolV2MutationCases()
	if len(cases) < 30 {
		t.Fatalf("only %d mutation cases, want >= 30", len(cases))
	}
	for _, c := range cases {
		in := baseProtocolV2Input()
		c.apply(&in)
		mut, err := buildProtocolV2(in)
		if err != nil {
			t.Fatalf("%s: build: %v", c.name, err)
		}
		switch c.class {
		case "QUALITY":
			// I05: a quality-class field change MUST change the quality hash
			// (protocol variable hit rate 100%).
			if mut.QualityHash == base.QualityHash {
				t.Fatalf("%s (QUALITY): quality hash unchanged (missed)", c.name)
			}
			if r := CompareQualityProtocol(base, mut); r.Status == Comparable {
				t.Fatalf("%s (QUALITY): comparator reported COMPARABLE", c.name)
			}
		case "PERFORMANCE":
			// Performance-class changes must change the performance hash but NOT
			// pollute the quality hash.
			if mut.PerformanceHash == base.PerformanceHash {
				t.Fatalf("%s (PERFORMANCE): performance hash unchanged (missed)", c.name)
			}
			if mut.QualityHash != base.QualityHash {
				t.Fatalf("%s (PERFORMANCE): quality hash polluted", c.name)
			}
			if r := ComparePerformanceProtocol(base, mut); r.Status != NotComparablePerformance {
				t.Fatalf("%s (PERFORMANCE): performance comparator = %s", c.name, r.Status)
			}
			if r := CompareQualityProtocol(base, mut); r.Status != Comparable {
				t.Fatalf("%s (PERFORMANCE): quality comparator = %s, want COMPARABLE", c.name, r.Status)
			}
		case "PROVENANCE":
			// Provenance-only changes change the provenance hash and must NOT
			// invalidate a quality or performance comparison (false invalidation = 0).
			if mut.ProvenanceHash == base.ProvenanceHash {
				t.Fatalf("%s (PROVENANCE): provenance hash unchanged (missed)", c.name)
			}
			if mut.QualityHash != base.QualityHash || mut.PerformanceHash != base.PerformanceHash {
				t.Fatalf("%s (PROVENANCE): provenance-only change polluted quality/performance hash", c.name)
			}
			if r := CompareQualityProtocol(base, mut); r.Status != Comparable {
				t.Fatalf("%s (PROVENANCE): quality comparator = %s, want COMPARABLE", c.name, r.Status)
			}
			if r := ComparePerformanceProtocol(base, mut); r.Status != Comparable {
				t.Fatalf("%s (PROVENANCE): performance comparator = %s, want COMPARABLE", c.name, r.Status)
			}
		default:
			t.Fatalf("%s: unknown class %q", c.name, c.class)
		}
	}
}

// mutateContract returns a copy of p with the top-level measurement contract hash
// replaced (simulating F05: metric artifact edited by 1 byte, or a missing
// UNVERSIONED contract for I14).
func mutateContract(p *ProtocolV2, hash string) *ProtocolV2 {
	cp := *p
	cp.MeasurementContractHash = hash
	return &cp
}

func TestCompareQualityProtocolEnumStates(t *testing.T) {
	base, _ := buildProtocolV2(baseProtocolV2Input())

	pipelineDiff := func() *ProtocolV2 {
		in := baseProtocolV2Input()
		in.ParserVersion = "parser/999"
		p, _ := buildProtocolV2(in)
		return p
	}
	datasetDiff := func() *ProtocolV2 {
		in := baseProtocolV2Input()
		in.DatasetID = "ds-other"
		p, _ := buildProtocolV2(in)
		return p
	}
	perfDiff := func() *ProtocolV2 {
		in := baseProtocolV2Input()
		in.Concurrency = 99
		p, _ := buildProtocolV2(in)
		return p
	}

	cases := []struct {
		name string
		a, b *ProtocolV2
		want Comparability
	}{
		// COMPARABLE (>=3)
		{"comparable_identical", base, base, Comparable},
		{"comparable_provenance_only", base, func() *ProtocolV2 {
			in := baseProtocolV2Input()
			in.Provenance.GitCommit = "changed"
			p, _ := buildProtocolV2(in)
			return p
		}(), Comparable},
		{"comparable_performance_only_quality_view", base, perfDiff(), Comparable},
		// NOT_COMPARABLE_DATASET (>=3)
		{"dataset_id", base, datasetDiff(), NotComparableDataset},
		{"dataset_content_hash", base, func() *ProtocolV2 {
			in := baseProtocolV2Input()
			in.DatasetContentHash = digestV2("other")
			p, _ := buildProtocolV2(in)
			return p
		}(), NotComparableDataset},
		{"dataset_id_and_hash", base, func() *ProtocolV2 {
			in := baseProtocolV2Input()
			in.DatasetID = "ds-x"
			in.DatasetContentHash = digestV2("x")
			p, _ := buildProtocolV2(in)
			return p
		}(), NotComparableDataset},
		// NOT_COMPARABLE_PIPELINE (>=3)
		{"pipeline_parser", base, pipelineDiff(), NotComparablePipeline},
		{"pipeline_index_backend", base, func() *ProtocolV2 {
			in := baseProtocolV2Input()
			in.IndexBackend = "other"
			p, _ := buildProtocolV2(in)
			return p
		}(), NotComparablePipeline},
		{"pipeline_temperature", base, func() *ProtocolV2 {
			in := baseProtocolV2Input()
			in.GenerationParams.Temperature = 0.9
			p, _ := buildProtocolV2(in)
			return p
		}(), NotComparablePipeline},
		// NOT_COMPARABLE_METRIC (>=3): both sides valid but different contracts.
		{"metric_different", base, mutateContract(base, digestV2("metric-v2")), NotComparableMetric},
		{"metric_different_same_dataset", base, mutateContract(base, digestV2("metric-v3")), NotComparableMetric},
		{"metric_different_plus_pipeline", pipelineDiff(), mutateContract(pipelineDiff(), digestV2("metric-v2")), NotComparableMetric},
		// NOT_COMPARABLE_MEASUREMENT (>=3): a side lacks a valid contract.
		{"measurement_empty_a", mutateContract(base, ""), base, NotComparableMeasurement},
		{"measurement_empty_b", base, mutateContract(base, ""), NotComparableMeasurement},
		{"measurement_unversioned", base, mutateContract(base, "UNVERSIONED"), NotComparableMeasurement},
		// NOT_COMPARABLE_PERFORMANCE_PROFILE (>=3)
		{"performance_concurrency", base, perfDiff(), NotComparablePerformance},
		{"performance_hardware", base, func() *ProtocolV2 {
			in := baseProtocolV2Input()
			in.HardwareClass = "x"
			p, _ := buildProtocolV2(in)
			return p
		}(), NotComparablePerformance},
		{"performance_cache_state", base, func() *ProtocolV2 {
			in := baseProtocolV2Input()
			in.CacheState = "hot"
			p, _ := buildProtocolV2(in)
			return p
		}(), NotComparablePerformance},
		// ERROR (>=3)
		{"error_nil_a", nil, base, ComparabilityError},
		{"error_nil_b", base, nil, ComparabilityError},
		{"error_missing_schema", &ProtocolV2{}, base, ComparabilityError},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got ComparabilityResult
			switch c.want {
			case NotComparablePerformance:
				got = ComparePerformanceProtocol(c.a, c.b)
			default:
				got = CompareQualityProtocol(c.a, c.b)
			}
			if got.Status != c.want {
				t.Fatalf("status = %s, want %s (mismatches=%v)", got.Status, c.want, got.MismatchedFields)
			}
			if got.Status != Comparable && len(got.MismatchedFields) == 0 {
				t.Fatalf("non-comparable result %s must list mismatched_fields", got.Status)
			}
		})
	}
}

// TestProtocolV2SecretFree verifies the snapshot JSON contains no secret-bearing
// material: the raw endpoint and a raw API key that went INTO digestV2 must never
// appear in the output (I06, AC10).
func TestProtocolV2SecretFree(t *testing.T) {
	rawEndpoint := "https://embed.example.com/v1?api_key=sk-live-1234567890"
	rawParams := `{"authorization":"Bearer topsecret","password":"hunter2"}`
	in := baseProtocolV2Input()
	in.ModelFingerprints[0].EndpointDigest = digestV2(rawEndpoint)
	in.ModelFingerprints[0].ParamsDigest = digestV2(rawParams)
	in.Provenance.EndpointHostDigest = digestV2(rawEndpoint)

	p, err := buildProtocolV2(in)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(raw)

	forbidden := []string{
		rawEndpoint,
		"sk-live-1234567890",
		"Bearer topsecret",
		"hunter2",
		"api_key",
		"Authorization",
		"password",
		"prompt", // no plaintext prompt fragments
	}
	for _, f := range forbidden {
		if strings.Contains(out, f) {
			t.Fatalf("secret material leaked into snapshot: %q", f)
		}
	}
	if !strings.Contains(out, digestV2(rawEndpoint)) {
		t.Fatalf("endpoint digest missing from snapshot")
	}
	if !strings.Contains(out, digestV2(rawParams)) {
		t.Fatalf("params digest missing from snapshot")
	}
}

// TestProtocolV2SchemaVersionIsV2 guards against accidentally reverting to the
// unversioned v1 contract (I04/I14).
func TestProtocolV2SchemaVersionIsV2(t *testing.T) {
	if evaluationProtocolSchemaVersionV2 != "evaluation_protocol/2" {
		t.Fatalf("schema version = %q", evaluationProtocolSchemaVersionV2)
	}
	if metric.MeasurementContractHash() == "" || metric.MeasurementContractHash() == "UNVERSIONED" {
		t.Fatalf("measurement contract must be non-UNVERSIONED")
	}
}
