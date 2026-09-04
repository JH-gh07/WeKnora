# Reproducibility & Benchmark Contract (Official Core)

> Applies to Release Candidate `0.8.0-rc1` and later. This document is the
> authoritative reference for the deterministic quality reproduction entry
> `make reproduce-evaluation`. It is intentionally minimal (see the Task009/G7
> plan for the full governance context) and does not duplicate constants that
> already live as the single source of truth in `tests/evaluation/**`.

## 1. What this is

`make reproduce-evaluation` reproduces the deterministic retrieval quality
regression (Task007/G5 gate) from a fresh clone, with **zero network, provider,
secret, database, Docker, or UI** during the execution phase. It is not a second
retrieval implementation: it reuses the single computation authority
`cmd/evaluation-regression` (subcommands `run`, `compare`,
`validate-ranking-seam`) and the versioned inputs under `tests/evaluation/`.

> **Important — bootstrap is not the execution phase.** The public command also
> builds that CLI. On a cold machine, Go may use the network to obtain the exact
> toolchain selected by `go.mod` and modules pinned by `go.sum`. The build is
> offline only when the matching toolchain and module cache are already present.
> This release does not vendor the complete dependency graph and therefore does
> not claim that a cold clone with an empty cache can build while disconnected.

## 2. Two reproduction contracts

| Contract | Scope | Status | Blocking? |
| --- | --- | --- | --- |
| **Official Core** | offline deterministic retrieval metrics vs immutable baseline | mandatory | yes (P0) |
| **Provider Experiment** | live model/embedding boundary observation | advisory, opt-in | no |

The Provider Experiment is a **separate opt-in entry**, is not triggered by the
default command, and requires an explicit credential/budget decision. With no
credential configured it must report `SKIP_WITH_REASON` / `INCONCLUSIVE` and
never contaminate the Official Core conclusion. This repository does not ship a
default provider path in `make reproduce-evaluation`.

## 3. One command

```bash
make reproduce-evaluation
# or, to choose the output directory:
OUTPUT_DIR=/tmp/my-run make reproduce-evaluation
```

The command:

1. performs the bootstrap build of `cmd/evaluation-regression` into a disposable
   temp directory (no root binary residue); this step may download the pinned Go
   toolchain/modules on a cold machine;
2. validates the production ranking seam (SUT)
   `internal/application/service/knowledgebase_search_fusion.go` and records its
   `ranking_artifact_hash`;
3. runs the deterministic fixture and writes a candidate result;
4. compares the candidate against the protected baseline `B001` and writes the
   decision;
5. emits a stable machine-readable `summary.json` plus a human-readable
   `summary.md`.

### 3.1 Bootstrap choices

Choose one before testing the offline execution claim:

1. allow network access for the first `make reproduce-evaluation` build; or
2. pre-warm the matching Go toolchain and `GOMODCACHE` from the same commit; or
3. use a prepared build image/cache whose identity is recorded in the
   environment report.

For example, this warms the exact command package without leaving a binary in
the repository:

```bash
build_dir="$(mktemp -d)"
go build -o "$build_dir/evaluation-regression" ./cmd/evaluation-regression/
rm -rf "$build_dir"
```

After bootstrap, run the public command with network egress disabled and the
same toolchain/module cache mounted read-only or otherwise preserved. A missing
toolchain/module in that phase is an infrastructure error:

```text
ERROR/4 (build_failed)
```

It is not `BLOCK`, and it says nothing about retrieval quality.

## 4. Frozen inputs (single source of truth)

| Input | Path |
| --- | --- |
| fixture | `tests/evaluation/fixtures/retrieval_core_v1.json` |
| comparison policy | `tests/evaluation/policies/quality_core_v1.json` |
| evaluator contract | `tests/evaluation/evaluator_contract.json` |
| evaluator artifact manifest | `tests/evaluation/evaluator_artifact_manifest.json` |
| ranking seam (SUT) | `internal/application/service/knowledgebase_search_fusion.go` |
| baseline | `tests/evaluation/baselines/baseline_B001_manifest.json` |

Do not copy expected metric constants into this document: the baseline manifest
is authoritative. Any change to these assets is a Measurement Change Review, not
a silent edit.

## 5. Exit / decision contract

| Decision | Exit code | Meaning |
| --- | --- | --- |
| `PASS` | 0 | no metric regression vs baseline |
| `BLOCK` | 2 | confirmed quality regression |
| `NOT_COMPARABLE` | 3 | preflight / identity mismatch; comparison refused |
| `ERROR` | 4 | infrastructure or execution failure |
| (usage) | 1 | flag/usage error, never reported as success |

The public entry never exits `1` on an infrastructure failure. Missing tools
(`git`/`go`/`python3`/`jq`/…), a failed `go build`, a failed
`validate-ranking-seam`/`run`, or a failed Python aggregation are all mapped to
`ERROR/4` with a reason-coded message (`reason=missing_tool|build_failed|…`).
Only the `compare` step's own exit (0/2/3/4) is propagated as the final code; a
`compare` exit outside that set is also treated as `ERROR/4`.

The entry is locale-safe: it selects a real UTF-8 locale that exists on the
host, forces `PYTHONUTF8=1`/`PYTHONIOENCODING=utf-8`, and runs the stdlib-only
aggregation with `python3 -S` so a site-packages `.pth` file containing a
non-ASCII path can never crash the run (see §9 non-UTF-8 locale test).

## 6. Output artifact set

`reproduction-output/<run-id>/` (or `OUTPUT_DIR`) contains:

```text
summary.json            machine-readable summary + decision
summary.md              human-readable summary
environment_lock.json   os/arch/go/locale/db/container provenance
source_identity.json    commit/tree + fixture/policy/evaluator/ranking identity
input_manifest.json     sha256 of the exact inputs consumed
candidate_result.json   deterministic runner result
comparison_decision.json  comparator decision
ranking_artifact.txt    ranking seam identity (validate-ranking-seam)
artifact_manifest.tsv   per-file sha256 + __ROOT__ digest (self-excluded)
commands.log            command trace
stderr.log              stderr capture
```

`artifact_manifest.tsv` root digest is computed as SHA-256 over the sorted
concatenation `"FILE <path>\n<bytes>"` of every other file in the run dir.

## 7. Result comparison rules

| Result type | P0 rule | Reporting |
| --- | --- | --- |
| deterministic retrieval metrics | numeric `epsilon` match vs baseline (from policy) | metric + delta + epsilon |
| canonical machine artifact | exact byte/hash match | expected vs actual hash |
| stochastic generation | out of Official Core scope | tolerance or INCONCLUSIVE |
| latency / cost | advisory only | workload/sample/p50-p95/MAD + environment |
| provider cache | advisory | provider/model/revision/time window |

## 8. Network independence

The one-command entry contains two distinct phases:

| Phase | Includes | Network contract |
| --- | --- | --- |
| Bootstrap | resolve/present Go toolchain and modules; `go build` the CLI | may require network on a cold machine; otherwise use a recorded pre-warmed cache |
| Deterministic execution | `validate-ranking-seam`, `run`, `compare`, stdlib-only aggregation | no network/provider/secret/DB dependency |

The execution phase performs no network I/O by executable dependency boundary;
the runner additionally rejects the SUT seam if it contains
network/provider/db/subprocess/file-write/init content. The authoritative E1
environment also disables egress during this phase. On platforms where a hard
egress sandbox cannot be asserted, the weaker evidence must be recorded as
"network independence proven by executable dependency boundary + clean
environment", not as an egress-sandbox claim.

Consequently the supported statement is:

> Fresh clone + one public command reproduces the result after dependency
> bootstrap; the deterministic execution phase is offline.

The unsupported statement is:

> A completely cold machine with an empty dependency cache builds and executes
> offline from the first byte.

## 9. Negative contract tests

The following are exercised against the entry (see the G7 verification plan):

- missing fixture → `ERROR/4`
- fixture hash mismatch → `NOT_COMPARABLE/3`
- unknown policy/version → `NOT_COMPARABLE/3`
- controlled deterministic quality regression → `BLOCK/2`
- existing non-empty output dir → fail closed
- `.env` with a fake provider key → behavior/output unchanged, key not read or printed
- empty/one-time `HOME` and `env -i` → still succeeds
- non-default `LC_ALL`/`LANG` (e.g. `LC_ALL=C`, `C.UTF-8`, `POSIX`) with a non-ASCII
  site-packages `.pth` path → run still succeeds, Chinese paths and JSON output
  uncorrupted (Python site import must not crash with `UnicodeDecodeError`)
- repository path with spaces / Chinese characters → succeeds
- no network → succeeds
- absent adjacent `status/` directory → succeeds
- post-run tracked tree has zero diff and no undeclared residue
