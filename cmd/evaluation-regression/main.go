// Command evaluation-regression is the stable, offline machine interface for
// the Task007 (G5) deterministic quality regression gate. It is reused by the
// required-check workflow and (later) by Task009 official core reproduction.
//
// Subcommands:
//
//	run                    produce a candidate result artifact from a frozen fixture
//	compare                preflight + comparator against an immutable baseline
//	validate-ranking-seam  AST-validate and identify the candidate SUT source
//
// Exit codes follow the four-state contract: PASS=0, BLOCK=2,
// NOT_COMPARABLE=3, ERROR=4. exit 1 is reserved for flag/usage errors.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Tencent/WeKnora/internal/application/service"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:])
	case "compare":
		return cmdCompare(args[1:])
	case "validate-ranking-seam":
		return cmdValidateRankingSeam(args[1:])
	default:
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  evaluation-regression run     --fixture F --policy P --contract C --manifest M --ranking-file R --out O
  evaluation-regression compare --baseline B --candidate C --policy P --out O
  evaluation-regression validate-ranking-seam --ranking-file R`)
}

func cmdValidateRankingSeam(args []string) int {
	fs := flag.NewFlagSet("validate-ranking-seam", flag.ExitOnError)
	var rankingFile string
	fs.StringVar(&rankingFile, "ranking-file", "", "path to the production ranking seam source (SUT)")
	_ = fs.Parse(args)
	if rankingFile == "" {
		usage()
		return 1
	}
	hash, err := service.RankingSeamIdentity(rankingFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ranking seam: %v\n", err)
		return 4
	}
	fmt.Printf("ranking_artifact_hash=%s\n", hash)
	return 0
}

// evaluatorContract is the frozen evaluator/metric identity injected at run
// time from a protected contract file (never from the candidate worktree).
type evaluatorContract struct {
	EvaluatorArtifactHash   string `json:"evaluator_artifact_hash"`
	MetricDefinitionVersion string `json:"metric_definition_version"`
}

// evaluatorManifest is the protected evaluator artifact manifest. component_files
// lists the measurement apparatus (runner + metric implementations); the ranking
// seam is deliberately NOT a component (it is the SUT, whose identity is
// recorded as ranking_artifact_hash via --ranking-file).
type evaluatorManifest struct {
	EvaluatorArtifactHash   string   `json:"evaluator_artifact_hash"`
	MetricDefinitionVersion string   `json:"metric_definition_version"`
	ComponentFiles          []string `json:"component_files"`
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var fixturePath, policyPath, contractPath, manifestPath, rankingFile, outPath string
	fs.StringVar(&fixturePath, "fixture", "", "path to versioned fixture JSON")
	fs.StringVar(&policyPath, "policy", "", "path to comparison policy JSON")
	fs.StringVar(&contractPath, "contract", "", "path to evaluator contract JSON (evaluator_artifact_hash + metric_definition_version)")
	fs.StringVar(&manifestPath, "manifest", "", "path to evaluator artifact manifest JSON (component_files)")
	fs.StringVar(&rankingFile, "ranking-file", "", "path to the production ranking seam source (SUT)")
	fs.StringVar(&outPath, "out", "", "output path for candidate result JSON")
	_ = fs.Parse(args)

	if fixturePath == "" || policyPath == "" || contractPath == "" || manifestPath == "" || rankingFile == "" || outPath == "" {
		usage()
		return 1
	}

	fixtureRaw, err := os.ReadFile(fixturePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		return 4
	}
	fixture, err := service.ParseRegressionFixture(fixtureRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fixture: %v\n", err)
		return 4
	}

	policyRaw, err := os.ReadFile(policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy: %v\n", err)
		return 4
	}
	policy, err := service.ParseComparisonPolicy(policyRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy: %v\n", err)
		return 4
	}

	contractRaw, err := os.ReadFile(contractPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "contract: %v\n", err)
		return 4
	}
	var contract evaluatorContract
	if err := json.Unmarshal(contractRaw, &contract); err != nil {
		fmt.Fprintf(os.Stderr, "contract: %v\n", err)
		return 4
	}
	if contract.EvaluatorArtifactHash == "" || contract.MetricDefinitionVersion == "" {
		fmt.Fprintln(os.Stderr, "contract: missing evaluator_artifact_hash or metric_definition_version")
		return 4
	}

	manifestRaw, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "manifest: %v\n", err)
		return 4
	}
	var manifest evaluatorManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		fmt.Fprintf(os.Stderr, "manifest: %v\n", err)
		return 4
	}
	if len(manifest.ComponentFiles) == 0 {
		fmt.Fprintln(os.Stderr, "manifest: component_files is empty")
		return 4
	}

	// P0-1: VERIFY the evaluator identity instead of self-reporting it. Recompute
	// the evaluator hash from the actual component files on disk and require it
	// to match the frozen contract hash. Any tampering with the measurement
	// apparatus (runner/metric) fails closed here.
	computedEvaluator, err := service.EvaluatorArtifactHash(manifest.ComponentFiles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evaluator hash: %v\n", err)
		return 4
	}
	if computedEvaluator != contract.EvaluatorArtifactHash {
		fmt.Fprintf(os.Stderr, "evaluator identity mismatch: computed %s != contract %s\n", computedEvaluator, contract.EvaluatorArtifactHash)
		return 4
	}
	if manifest.EvaluatorArtifactHash != contract.EvaluatorArtifactHash {
		fmt.Fprintf(os.Stderr, "manifest evaluator_artifact_hash %s != contract %s\n", manifest.EvaluatorArtifactHash, contract.EvaluatorArtifactHash)
		return 4
	}
	if manifest.MetricDefinitionVersion != contract.MetricDefinitionVersion {
		fmt.Fprintf(os.Stderr, "manifest metric_definition_version %q != contract %q\n", manifest.MetricDefinitionVersion, contract.MetricDefinitionVersion)
		return 4
	}

	// SUT identity: the ranking seam is ALLOWED to differ across candidates (that
	// is the point of the gate), but its identity is recorded as provenance. The
	// SUT boundary is enforced BEFORE hashing: a seam with network/provider/db/
	// subprocess/file-write/init content is rejected fail-closed (exit 4) and can
	// never be recorded as a comparable ranking identity.
	rankingHash, err := service.RankingSeamIdentity(rankingFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ranking seam: %v\n", err)
		return 4
	}

	result, err := service.RunRegressionFixture(
		context.Background(),
		fixture,
		computedEvaluator,
		contract.MetricDefinitionVersion,
		service.ComparisonPolicyHash(policy),
		rankingHash,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return 4
	}
	if err := writeJSON(outPath, result); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		return 4
	}
	fmt.Printf("result written: %s\n", outPath)
	return 0
}

func cmdCompare(args []string) int {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	var baselinePath, candidatePath, policyPath, outPath string
	fs.StringVar(&baselinePath, "baseline", "", "path to immutable baseline manifest JSON")
	fs.StringVar(&candidatePath, "candidate", "", "path to candidate result JSON")
	fs.StringVar(&policyPath, "policy", "", "path to comparison policy JSON")
	fs.StringVar(&outPath, "out", "", "output path for comparison JSON")
	_ = fs.Parse(args)

	if baselinePath == "" || candidatePath == "" || policyPath == "" || outPath == "" {
		usage()
		return 1
	}

	baselineRaw, err := os.ReadFile(baselinePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline: %v\n", err)
		return 4
	}
	baseline, err := service.ParseBaselineManifest(baselineRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline: %v\n", err)
		return 4
	}

	candidateRaw, err := os.ReadFile(candidatePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "candidate: %v\n", err)
		return 4
	}
	candidate, err := service.ParseRegressionResult(candidateRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "candidate: %v\n", err)
		return 4
	}

	policyRaw, err := os.ReadFile(policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy: %v\n", err)
		return 4
	}
	policy, err := service.ParseComparisonPolicy(policyRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "policy: %v\n", err)
		return 4
	}

	comparison := service.RunComparator(baseline, candidate, policy)
	if err := writeJSON(outPath, comparison); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		return 4
	}
	fmt.Printf("decision=%s (exit %d) written: %s\n", comparison.Decision, comparison.Decision.ExitCode(), outPath)
	return comparison.Decision.ExitCode()
}

func writeJSON(path string, v any) error {
	// Compact marshal with NO trailing newline keeps the artifact byte-identical
	// to the canonical hash inputs (ResultArtifactHash / policy hash), so the
	// file's shasum equals the identity hash for byte-level reproduction.
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
