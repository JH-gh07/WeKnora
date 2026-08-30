#!/usr/bin/env bash
# WeKnora — Official Core Reproduction (Task009 / G7, Release Candidate readiness)
#
# One command, offline deterministic quality reproduction. It reuses the single
# computation authority cmd/evaluation-regression (run / compare /
# validate-ranking-seam) and the versioned tests/evaluation/** inputs. It does
# NOT copy the comparator or metric implementations.
#
# Contract (see docs/reproducibility.md):
#   * no network / provider / secret / DB / Docker / UI during execution
#   * does not read .env, provider keys, user HOME config, or git global alias
#   * non-interactive; exit code mirrors the four-state contract:
#       PASS=0  BLOCK=2  NOT_COMPARABLE=3  ERROR=4  (1 = usage)
#   * OUTPUT_DIR=<new dir> is honored; an existing non-empty OUTPUT_DIR fails closed
#   * default output lands in reproduction-output/<run-id> (never overwrites)
#
# Usage:
#   make reproduce-evaluation
#   bash scripts/reproduce-evaluation.sh
#   OUTPUT_DIR=/tmp/my-run bash scripts/reproduce-evaluation.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# ---- versioned default inputs (frozen; never read from env/profile) ----
FIXTURE="tests/evaluation/fixtures/retrieval_core_v1.json"
POLICY="tests/evaluation/policies/quality_core_v1.json"
CONTRACT="tests/evaluation/evaluator_contract.json"
MANIFEST="tests/evaluation/evaluator_artifact_manifest.json"
RANKING_FILE="internal/application/service/knowledgebase_search_fusion.go"
BASELINE="tests/evaluation/baselines/baseline_B001_manifest.json"

for f in "$FIXTURE" "$POLICY" "$CONTRACT" "$MANIFEST" "$RANKING_FILE" "$BASELINE"; do
  if [[ ! -f "$f" ]]; then
    echo "ERROR(reason=missing_input): required input not found: $f" >&2
    exit 4
  fi
done

# ---- output directory (fail closed on existing non-empty dir) ----
if [[ -n "${OUTPUT_DIR:-}" ]]; then
  OUT="$OUTPUT_DIR"
  if [[ -e "$OUT" ]] && [[ -n "$(ls -A "$OUT" 2>/dev/null)" ]]; then
    echo "ERROR(reason=output_dir_not_empty): OUTPUT_DIR exists and is non-empty: $OUT" >&2
    exit 4
  fi
else
  RUN_ID="run-$(date -u +%Y%m%dT%H%M%SZ)"
  OUT="reproduction-output/$RUN_ID"
  if [[ -e "$OUT" ]]; then
    echo "ERROR(reason=output_dir_exists): $OUT already exists" >&2
    exit 4
  fi
fi
mkdir -p "$OUT"

COMMANDS_LOG="$OUT/commands.log"
STDERR_LOG="$OUT/stderr.log"
: > "$COMMANDS_LOG"
: > "$STDERR_LOG"

log_cmd() {
  printf '%s\n' "$*" >> "$COMMANDS_LOG"
}

START_EPOCH="$(date +%s)"
START_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
COMMIT="$(git rev-parse HEAD)"
TREE="$(git rev-parse HEAD^{tree})"
OS_NAME="$(uname -s)"
OS_ARCH="$(uname -m)"
GO_VERSION="$(go version 2>/dev/null || echo NOT_AVAILABLE)"

# ---- build the CLI into a disposable temp dir (no root binary residue) ----
BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/weknora-eval.XXXXXX")"
cleanup() { rm -rf "$BUILD_DIR"; }
trap cleanup EXIT

BIN="$BUILD_DIR/evaluation-regression"
{
  log_cmd "go build -o $BIN ./cmd/evaluation-regression/"
  go build -o "$BIN" ./cmd/evaluation-regression/
} 2>>"$STDERR_LOG"

# ---- validate the production ranking seam (SUT) identity ----
{
  log_cmd "$BIN validate-ranking-seam --ranking-file $RANKING_FILE"
  "$BIN" validate-ranking-seam --ranking-file "$RANKING_FILE" 2>>"$STDERR_LOG"
} > "$OUT/ranking_artifact.txt"
RANKING_HASH="$(sed -n 's/^ranking_artifact_hash=//p' "$OUT/ranking_artifact.txt")"

# ---- deterministic run -> candidate result ----
{
  log_cmd "$BIN run --fixture $FIXTURE --policy $POLICY --contract $CONTRACT --manifest $MANIFEST --ranking-file $RANKING_FILE --out $OUT/candidate_result.json"
  "$BIN" run \
    --fixture      "$FIXTURE" \
    --policy       "$POLICY" \
    --contract     "$CONTRACT" \
    --manifest     "$MANIFEST" \
    --ranking-file "$RANKING_FILE" \
    --out          "$OUT/candidate_result.json" 2>>"$STDERR_LOG"
} > "$OUT/run_stdout.txt"

# ---- comparison against protected baseline (propagate decision exit) ----
COMPARE_EXIT=0
{
  log_cmd "$BIN compare --baseline $BASELINE --candidate $OUT/candidate_result.json --policy $POLICY --out $OUT/comparison_decision.json"
  set +e
  "$BIN" compare \
    --baseline  "$BASELINE" \
    --candidate "$OUT/candidate_result.json" \
    --policy    "$POLICY" \
    --out       "$OUT/comparison_decision.json" 2>>"$STDERR_LOG"
  COMPARE_EXIT=$?
  set -e
} > "$OUT/compare_stdout.txt"

END_EPOCH="$(date +%s)"
END_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
DURATION=$((END_EPOCH - START_EPOCH))

# ---- aggregate provenance / summary / manifest (python3 is a standard tool) ----
export OUT COMMIT TREE START_UTC END_UTC DURATION RANKING_HASH \
       FIXTURE POLICY CONTRACT MANIFEST RANKING_FILE BASELINE \
       OS_NAME OS_ARCH GO_VERSION COMPARE_EXIT
python3 - "$OUT" <<'PYEOF'
import json, os, sys, hashlib, subprocess

out = sys.argv[1]
def read_json(p):
    with open(p) as fh:
        return json.load(fh)

candidate = read_json(os.path.join(out, "candidate_result.json"))
comparison = read_json(os.path.join(out, "comparison_decision.json"))
env = {k: os.environ.get(k, "") for k in
       ["COMMIT","TREE","START_UTC","END_UTC","DURATION","RANKING_HASH",
        "FIXTURE","POLICY","CONTRACT","MANIFEST","RANKING_FILE","BASELINE",
        "OS_NAME","OS_ARCH","GO_VERSION","COMPARE_EXIT"]}

def sha256(p):
    return hashlib.sha256(open(p, "rb").read()).hexdigest()

# input_manifest.json — hash the exact inputs the runner consumed
inputs = [env["FIXTURE"], env["POLICY"], env["CONTRACT"], env["MANIFEST"], env["RANKING_FILE"], env["BASELINE"]]
input_manifest = {
    "schema_version": "reproduction_input/1",
    "files": [{"path": p, "sha256": sha256(p), "size_bytes": os.path.getsize(p)} for p in inputs],
}
with open(os.path.join(out, "input_manifest.json"), "w") as fh:
    json.dump(input_manifest, fh, indent=2); fh.write("\n")

# environment_lock.json
environment_lock = {
    "schema_version": "reproduction_environment/1",
    "os": env["OS_NAME"],
    "arch": env["OS_ARCH"],
    "go_version": env["GO_VERSION"],
    "node_version": "NOT_APPLICABLE",
    "db": "NOT_APPLICABLE",
    "container": "NOT_APPLICABLE",
    "locale": os.environ.get("LANG", "NOT_SET"),
    "timezone_utc": "UTC",
    "network_during_execution": "disabled_by_executable_dependency_boundary",
}
with open(os.path.join(out, "environment_lock.json"), "w") as fh:
    json.dump(environment_lock, fh, indent=2); fh.write("\n")

# source_identity.json
source_identity = {
    "schema_version": "reproduction_source/1",
    "commit_full": env["COMMIT"],
    "tree_hash": env["TREE"],
    "ranking_artifact_hash": env["RANKING_HASH"],
    "fixture_id": candidate.get("fixture_id"),
    "fixture_artifact_hash": candidate.get("fixture_artifact_hash"),
    "protocol_hash": candidate.get("protocol_hash"),
    "evaluator_artifact_hash": candidate.get("evaluator_artifact_hash"),
    "metric_definition_version": candidate.get("metric_definition_version"),
    "comparison_policy_hash": candidate.get("comparison_policy_hash"),
    "baseline_id": comparison.get("baseline_id"),
    "policy_id": comparison.get("policy_id"),
}
with open(os.path.join(out, "source_identity.json"), "w") as fh:
    json.dump(source_identity, fh, indent=2); fh.write("\n")

decision = comparison.get("decision", "ERROR")
preflight = comparison.get("preflight", {})
metrics = comparison.get("metrics", [])

summary = {
    "schema_version": "reproduction_summary/1",
    "command": "make reproduce-evaluation",
    "start_utc": env["START_UTC"],
    "end_utc": env["END_UTC"],
    "duration_seconds": int(env["DURATION"]),
    "exit_code": int(env["COMPARE_EXIT"]),
    "decision": decision,
    "preflight_decision": preflight.get("decision"),
    "preflight_reason_codes": preflight.get("reason_codes", []),
    "failed_rules": comparison.get("failed_rules", []),
    "commit_full": env["COMMIT"],
    "tree_hash": env["TREE"],
    "baseline_id": comparison.get("baseline_id"),
    "policy_id": comparison.get("policy_id"),
    "metrics": metrics,
}
with open(os.path.join(out, "summary.json"), "w") as fh:
    json.dump(summary, fh, indent=2); fh.write("\n")

# human-readable summary.md
lines = [
    "# Official Core Reproduction Summary",
    "",
    "| field | value |",
    "| --- | --- |",
    f"| decision | {decision} |",
    f"| exit_code | {env['COMPARE_EXIT']} |",
    f"| commit_full | {env['COMMIT']} |",
    f"| tree_hash | {env['TREE']} |",
    f"| fixture_id | {source_identity['fixture_id']} |",
    f"| baseline_id | {comparison.get('baseline_id')} |",
    f"| policy_id | {comparison.get('policy_id')} |",
    f"| ranking_artifact_hash | {env['RANKING_HASH']} |",
    f"| start_utc | {env['START_UTC']} |",
    f"| end_utc | {env['END_UTC']} |",
    f"| duration_seconds | {env['DURATION']} |",
    f"| os/arch | {env['OS_NAME']}/{env['OS_ARCH']} |",
    f"| go_version | {env['GO_VERSION']} |",
    "",
    "## metric comparison (expected=baseline vs actual=candidate)",
    "",
    "| metric | baseline | candidate | delta | decision |",
    "| --- | --- | --- | --- | --- |",
]
for m in metrics:
    lines.append(f"| {m['metric']} | {m['baseline']} | {m['candidate']} | {m['delta']} | {m['decision']} |")
if not metrics:
    lines.append("| (none) | | | | |")
lines.append("")
with open(os.path.join(out, "summary.md"), "w") as fh:
    fh.write("\n".join(lines) + "\n")

# artifact_manifest.tsv — all produced artifacts, self-excluded from root
artifacts = sorted(f for f in os.listdir(out) if os.path.isfile(os.path.join(out, f)))
rows = []
h = hashlib.sha256()
for f in artifacts:
    if f == "artifact_manifest.tsv":
        continue
    p = os.path.join(out, f)
    b = open(p, "rb").read()
    h.update(("FILE %s\n" % f).encode()); h.update(b)
    rows.append((f, hashlib.sha256(b).hexdigest(), len(b)))
header = ["# Official Core reproduction artifact manifest",
          "# Scope: all files in this run dir EXCEPT artifact_manifest.tsv.",
          "# Method: per-file SHA-256 (col 2); __ROOT__ = SHA-256 over sorted 'FILE <path>\\n<bytes>'.",
          "path\tsha256\tsize_bytes"]
lines = list(header)
for f, dig, n in rows:
    lines.append(f"{f}\t{dig}\t{n}")
lines.append(f"__ROOT__\t{h.hexdigest()}\t{len(rows)}")
with open(os.path.join(out, "artifact_manifest.tsv"), "w") as fh:
    fh.write("\n".join(lines) + "\n")
PYEOF

# ---- human summary on stdout ----
echo "===================================================================="
echo "Official Core Reproduction — $(jq -r .decision "$OUT/comparison_decision.json" 2>/dev/null || echo '?')"
echo "  output dir : $OUT"
echo "  commit     : $COMMIT"
echo "  tree       : $TREE"
echo "  exit code  : $COMPARE_EXIT (PASS=0 BLOCK=2 NOT_COMPARABLE=3 ERROR=4)"
echo "  artifacts  : $(ls "$OUT" | tr '\n' ' ')"
echo "===================================================================="

exit "$COMPARE_EXIT"
