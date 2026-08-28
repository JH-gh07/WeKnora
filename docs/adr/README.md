# Architecture Decision Registry

```yaml
document_type: ADR Registry
version: 0.2
status: ACTIVE
created_at: 2026-08-25
architecture_source: ../requirement_matrix.md
external_delivery_blueprint: ../../../status/Imagination/最终理想项目的结构.md
current_implementation_identity: c089ab07
current_implementation_tag: gate/g1-g2a-implementation-20260825
```

This registry records architecture decisions that affect more than one module, establish a long-lived fact boundary, or would be expensive to reverse. It is intentionally an index plus compact canonical decisions—not a requirement to create one file for every implementation detail.

The delivery blueprint and runtime evidence currently live outside the upstream `WeKnora/` repository under the sibling `status/` workspace. Before an upstream PR or release, every accepted decision must also be traceable to committed code and repository tests; an external evidence path alone is not a release identity.

## Status model

| Status | Meaning |
| --- | --- |
| `PROPOSED` | Awaiting source or experiment evidence in its owning Gate |
| `ACCEPTED_DESIGN` | Semantic contract approved; implementation Evidence is not complete |
| `ACCEPTED` | Implementation and behavior Evidence exist |
| `ACCEPTED_WITH_BOUNDARY` | Evidence exists only inside an explicit deployment/provider/data boundary |
| `SUPERSEDED` | Replaced by another decision; history remains visible |

A Registry entry may move to `ACCEPTED` only when its owning test/Evidence is identifiable. A separate `NNNN-title.md` ADR is required when a decision gains multiple competing alternatives, a migration plan, or a superseding decision; until then, the compact entry here is canonical.

## Registry

| ID | Decision | Status | Primary owner | Revisit trigger |
| --- | --- | --- | --- | --- |
| ADR-001 | Separate `EvaluationProtocol` from `RunProvenance` | `ACCEPTED` | G1 | Comparison identity requirements change |
| ADR-002 | Exclude Git Commit from `protocol_hash` | `ACCEPTED` | G1 | Official evaluation identity requires commit isolation |
| ADR-003 | One `ModelCall` row represents one logical model abstraction invocation | `ACCEPTED` | G2A | P0 requires provider-attempt billing/audit |
| ADR-004 | Unknown/unreported/unsupported measurement is not numeric zero | `ACCEPTED` | G2A | Providers expose a uniform, provably complete contract |
| ADR-005 | Use startup reconciliation inside the current single-worker deployment | `ACCEPTED_WITH_BOUNDARY` | G1 | Multi-replica workers, MQ, lease, or scheduler is introduced |
| ADR-006 | Required regression CI uses deterministic core; provider experiments are separate | `ACCEPTED_DESIGN` | G5 | Official acceptance requires live-provider blocking |
| ADR-007 | Embedding Cache identity represents the computation, not only text | `ACCEPTED_DESIGN` | G3 | G3 source/experiment evidence selects the concrete key |
| ADR-008 | Do not introduce a global Model Gateway for P0 | `ACCEPTED` | G2A | Additional model families cause verified duplication or drift |
| ADR-009 | Local Embedding Cache and Provider Prompt Cache use separate facts and metrics | `ACCEPTED_DESIGN` | G3/G4 | A provider exposes a single verifiable semantic contract |
| ADR-010 | Availability state is separate from nullable metric value | `ACCEPTED_DESIGN` | G2B | API/UI compatibility review requires a versioned replacement |
| ADR-011 | Failure authority depends on fact class | `ACCEPTED_DESIGN` | G1–G6 | Runtime Evidence proves a safer replacement policy |
| ADR-012 | Latency budgets are declared before ON measurements | `ACCEPTED_DESIGN` | G3/G6 | Repository-wide SLO/performance policy supersedes it |
| ADR-013 | Deterministic retrieval gate reuses the production ranking seam with an explicit tie-breaker | `ACCEPTED_DESIGN` | G5 | Production ranking comparator semantics change |

## ADR-001 — Protocol and provenance are different facts

### Context

An evaluation needs both a comparison identity and an explanation of which implementation/environment produced a result. Mixing them makes identical experiments incomparable across commits.

### Decision

`EvaluationProtocol` contains resolved comparison inputs. `RunProvenance` contains Git Commit, application/environment identity, and observable model revision/deployment summaries.

### Consequences and boundary

- Comparison first checks Protocol identity, then uses Provenance to explain differences.
- Secrets and complete prompts never enter either snapshot; prompts use approved version/hash/length metadata.
- Evaluator artifact identity remains a known boundary until G5/G7 closes it.

### Evidence locator

Repository tests: `internal/application/service/evaluation_protocol_test.go`. Runtime delivery evidence: shared-workspace-relative `../../../status/evidence/task002/`.

## ADR-002 — Commit is not part of `protocol_hash`

### Context

Regression testing needs the same experiment definition to remain comparable when implementation code changes.

### Decision

Git Commit and application build identity belong to `RunProvenance`, not the canonical Protocol hash.

### Consequences and boundary

- Same Protocol + different Commit is eligible for a regression preflight, subject to evaluator/artifact compatibility.
- Different effective dataset/model/config/metric definition changes Protocol identity.
- Hash canonicalization must remain deterministic and secret-free.

### Evidence locator

`internal/application/service/evaluation_protocol.go` and its T3–T5 tests.

## ADR-003 — `ModelCall` has one logical-invocation grain

### Context

Mixing logical calls and provider attempts in one fact table makes `COUNT(ModelCall)`, success rate, latency, and cost ambiguous.

### Decision

One row represents one logical invocation of Chat, ChatStream, Embedding, or Rerank at the model abstraction boundary. Retry detail is nullable metadata; a future `ProviderAttempt` is a separate 1:N fact, not another `ModelCall` grain.

### Consequences and boundary

- `request_elapsed_ms` measures the logical invocation.
- `provider_latency_ms` is nullable and recorded only when the real provider request boundary is observable.
- `attempt_count` is nullable; observability is explicit.
- Batch item telemetry must not be inferred from row count.

### Evidence locator

`internal/types/model_call.go`, the three `internal/models/*/metering_wrapper.go` implementations, and their tests.

## ADR-004 — Unknown measurement is not zero

### Context

Provider telemetry, pricing, cache reporting, or metering persistence can be absent. Coercing absence to zero fabricates cost and hit-rate facts.

### Decision

Nullable values and categorical availability/finality are separate. `COMPLETE`, `PARTIAL`, and `UNKNOWN` describe measurement health; known totals exclude unknown-cost calls and expose their count.

### Consequences and boundary

- A real observed zero remains valid.
- Unsupported, unreported, unknown, and not implemented remain distinct.
- UI and reports must not render `UNKNOWN + 0` as a valid metric.
- Durable health events prevent known observation failures from being silently reported as complete.

### Evidence locator

`internal/types/model_call.go`, `internal/application/repository/model_call.go`, and repository/wrapper failure tests.

## ADR-005 — Single-worker startup reconciliation

### Context

The current Docker Compose deployment executes Evaluation in local goroutines without a distributed queue or lease owner. Process death leaves persisted incomplete Runs and temporary resources.

### Decision

At startup, the composition root invokes application reconciliation. It marks lost `RUNNING/PENDING` work as interrupted inside the current ownership boundary and retries discoverable temporary-resource cleanup without rewriting existing terminal states.

### Consequences and boundary

- The first release does not resume from an item checkpoint.
- Cleanup request acceptance is not misreported as physical deletion completion.
- This decision is invalidated by multi-replica execution, MQ, external workers, or lease ownership.

### Evidence locator

`cmd/server/bootstrap.go`, `internal/application/service/evaluation.go`, reconciliation tests, and shared-workspace-relative `../../../status/evidence/task002/`.

## ADR-006 — Deterministic required CI

### Context

Live provider latency, availability, generation, and reporting vary across runs and can turn merge protection into a flaky gate.

### Decision

The P0 required check uses a fixed local/deterministic fixture and blocks only on the declared quality regression policy. Real-provider cost, latency, and cache experiments are separately reproducible but advisory.

### Consequences and boundary

- CI needs no provider secret.
- A deliberately bad ranking/retrieval candidate must be blocked.
- Provider results remain valuable evidence but cannot be the required merge signal in P0.

### Evidence locator

Design is tracked by G5 and shared-workspace-relative `../../../status/evidence/task001/blocking_ci_feasibility.md`; implementation Evidence is pending.

## ADR-007 — Embedding Cache computation identity

### Context

The same text can produce different vectors across tenant, provider/model, dimensions, normalization, or preprocessing configuration.

### Proposed decision

A Cache key must represent the effective computation identity, not only a text hash. The exact key remains `PROPOSED` until G3 audits the provider and preprocessing boundaries.

### Required consequences

- Tenant isolation is the default.
- Model/config changes must cause a miss, not return stale vectors.
- Failed or dimension-invalid results are not written.
- TTL/LRU/GC and cross-tenant sharing are not implied by this decision.

## ADR-008 — Reuse model factories; no P0 global gateway

### Context

WeKnora has separate Chat, Embedding, and Rerank factories. Creating a universal gateway would be a large refactor without a demonstrated consumer need.

### Decision

Install the shared recorder contract as an outer decorator at each stable factory boundary. The decorator depends on `ModelCallRecorder`, not a concrete repository.

### Consequences and boundary

- Existing provider abstractions and business services remain intact.
- Ordinary model behavior is unchanged when no recorder is configured.
- A gateway may be reconsidered only when additional model families create measured duplication or semantic drift.

### Evidence locator

`internal/models/chat/chat.go`, `internal/models/embedding/embedder.go`, `internal/models/rerank/reranker.go`, and their metering wrappers/tests.

## ADR-009 — Local and provider cache facts stay separate

### Context

Local Embedding Cache hits avoid local provider-facing embedding calls. Provider Prompt Cache metrics describe token reuse reported by the provider. Their denominators and observability are different.

### Decision

Use separate facts and names for local item hit rate, provider reporting coverage, cached-token ratio, and reported-call hit rate. Never publish a generic unqualified `cache_hit_rate`.

### Consequences and boundary

- G2B displays local cache as `NOT_IMPLEMENTED` until G3 produces facts.
- Unsupported, unreported, and miss remain distinct for Provider Prompt Cache.
- G3 and G4 own separate experiments.

## ADR-010 — Availability and value are orthogonal

### Context

The UI is delivered before every metric source exists. A numeric zero cannot distinguish not implemented, unsupported, unreported, or unknown.

### Decision

Availability uses `AVAILABLE`, `NOT_IMPLEMENTED`, `UNSUPPORTED`, `UNREPORTED`, or `UNKNOWN`; metric values remain nullable. A non-available state cannot carry a non-null metric value.

### Consequences and boundary

- `AVAILABLE + 0` is a valid observed zero.
- `sample_count=0` does not prove the metric is zero.
- API, UI, and reports must share one truth-table fixture.
- G2B owns implementation verification; until then this is `ACCEPTED_DESIGN`.

## ADR-011 — Failure authority depends on fact class

### Context

Failing every operation closed would let metering or cache outages break successful business calls. Failing every operation open would create orphan resources, false completion, unauthorized access, or unreliable merge decisions.

### Decision

- Control facts—Run identity/lifecycle, authorization, baseline promotion, migration and comparison identity—fail closed.
- Observation persistence fails open for the business result but degrades Measurement Health to `PARTIAL/UNKNOWN`.
- Cache failures use only correctness-preserving provider fallback and never return unverified/corrupt data.
- Required governance checks fail closed to merge while distinguishing `BLOCK`, `NOT_COMPARABLE`, and `ERROR`.

### Consequences and boundary

- A persistence failure cannot be hidden as a successful durable fact.
- Metering failure cannot trigger a duplicate provider call merely to recreate telemetry.
- Cache fallback must retain tenant/model/computation identity and report degraded availability.
- Diagnosis may state attribution only when trace, diff, or controlled reproduction supports it.
- This is a semantic contract; each owning Gate still needs failure-injection Evidence.

### Evidence locator

Current Run and Metering evidence is shared-workspace-relative under `../../../status/evidence/task002/` and `../../../status/evidence/task003/`; Cache and Regression failure Evidence is pending G3/G5/G6.

## ADR-012 — Latency budget before measurement

### Context

Reporting p50/p95 after implementation does not define whether added latency is acceptable and allows thresholds to be selected after seeing the result. Summing overlapping or asynchronous spans also overstates critical-path overhead.

### Decision

Any Task adding synchronous work to a request path declares its fixture, SLO/headroom or engineering budget, absolute/relative limits, sample plan and failure action before viewing ON results. Final judgment uses paired OFF/ON deltas and only sums mutually exclusive critical-path spans.

### Consequences and boundary

- If no SLO/headroom or pre-approved budget exists, the verdict is `MEASURED / DECISION_PENDING`, not retroactive PASS.
- Local deterministic defaults are 5 warmups, 3 independent rounds and at least 30 measured samples per round, with raw samples, p50, p95 and MAD.
- Real-provider latency remains advisory and uses counter-balanced ordering with disclosed failures.
- Existing Task003 measurements remain historical facts; v0.2 does not invent a threshold after the run.
- A later repository-wide SLO/performance policy may supersede this compact decision.

## ADR-013 — Deterministic regression reuses the production ranking seam with an explicit tie-breaker

### Context

The required quality gate (R4, G5) must prove that a change to production retrieval code can genuinely change the reported metrics — otherwise the gate is vacuous. But an offline deterministic runner must not become a second, parallel retrieval implementation, and it must not depend on Go map iteration order, which is non-deterministic.

### Decision

The offline runner consumes a frozen fixture of precomputed scores and calls the production ranking/selection seam `fuseOrDeduplicate` (RRF fusion / score-desc dedup) directly, trusting its output order. Production `sortByScoreDesc` is made a total order by adding a `ChunkID`-ascending tie-breaker on equal score, removing the map-iteration non-determinism at its source.

### Consequences and boundary

- A regression in the production ranking seam (e.g. a wrong comparator direction, or a coverage collapse) now changes the runner's metrics and is caught by the comparator as BLOCK.
- The runner is a deterministic extraction of production, not a second retrieval implementation and not a hand-written metric comparison.
- The tie-breaker change is a production-code change (8 lines in `knowledgebase_search_fusion.go`) recorded as `ranking_artifact_hash` — the SUT identity, which is ALLOWED to differ across candidates — and is deliberately NOT part of the evaluator artifact (the evaluator is only the runner + metric implementations). Any change to the evaluator apparatus requires re-freezing the evaluator hash and re-verifying against the frozen contract; any change to ranking comparator semantics requires re-freezing the ranking identity and promoting a new baseline.
- The SUT is also a trust boundary, not just an identity: the candidate-owned ranking seam is constrained to a reviewed Go import allowlist and may not use import aliases, package-level variables, or `init()` side effects. `ValidateRankingSeamSource` enforces this with the Go AST before the seam is hashed (`RankingSeamIdentity`); the identity uses the frozen logical code path plus content, not the caller's checkout path. The trusted-base workflow invokes that validator before overlay and uses a NUL-safe `git diff --no-renames --name-status -z` classifier to fail closed (NOT_COMPARABLE) for measurement-apparatus or retrieval changes outside the SUT manifest. This is a source boundary, not a claim of host-level network sandboxing.
- Determinism is still bounded by Go float64 serialization (`go-json-float64/1`) and a fixed OS/arch reproduction rule.

### Evidence locator

Repository tests: `internal/application/service/evaluation_regression_test.go`. Runtime delivery evidence: shared-workspace-relative `../../../status/evidence/task007/`.

## Change protocol

To change an accepted decision:

1. Record the new source/runtime evidence and affected Requirement/Gate.
2. State whether the change is additive, behavioral, deprecated, or breaking.
3. Identify migrations and consumers.
4. Add a replacement ADR; mark the old entry `SUPERSEDED` rather than deleting it.
5. Re-run only the affected Gate Evidence—do not invalidate unrelated completed Tasks.

> **Reason for this registry (2026-08-25):** Architecture decisions previously existed across plans, Task reviews, and evidence files, but the upstream repository had no stable decision entry point. A compact canonical registry preserves the decisions and their revisit triggers without creating empty per-decision files or restructuring production code.
