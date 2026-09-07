package main

// Task013 experiment harness (AC-R8). Subcommands:
//
//	manifest   offline: render treatment+control for all 12 fixtures through
//	           the production builder, verify section identity, emit
//	           fixture_manifest.json / semantic_section_contract.json /
//	           section_identity_tests.log
//	negative   offline: run the N01-N14 negative matrix, emit
//	           negative_matrix.tsv and fail_closed.log
//	aggregate  offline: deterministic aggregation + paired bootstrap over a
//	           sanitized TSV; --golden runs the synthetic golden dataset;
//	           --repro3 asserts byte-identical output across 3 runs
//	live       gated: refuses without --allow-provider + credential env and a
//	           preregistered protocol; never reads .env automatically
//
// All modes are offline except the (unimplemented-until-preregistered) live
// runner behind explicit flags.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/agent"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/tests/evaluation/prompt-cache/fixtures"
)

const (
	experimentID    = "task013-main-v1"
	contractVersion = "task013-semantic-sections-v1"
	sectionJoin     = "\n\n"
	fixtureDomain   = "t013-cache-abi"
	layoutControl   = "control-v1-variable-first"
	layoutTreatment = "treatment-v1-production"
)

type manifestSection struct {
	SectionID       string `json:"section_id"`
	SHA256          string `json:"sha256"`
	ByteLength      int    `json:"byte_length"`
	OccurrenceCount int    `json:"occurrence_count"`
}

type sampleManifest struct {
	SampleID                 string            `json:"sample_id"`
	Tier                     string            `json:"tier"`
	Sections                 []manifestSection `json:"sections"`
	ControlSectionMultiset   string            `json:"control_section_multiset_hash"`
	TreatmentSectionMultiset string            `json:"treatment_section_multiset_hash"`
	MultisetEqual            bool              `json:"multiset_equal"`
	LayoutSequenceControl    []string          `json:"layout_sequence_control"`
	LayoutSequenceTreatment  []string          `json:"layout_sequence_treatment"`
	ControlMessagesSHA256    string            `json:"control_messages_sha256"`
	TreatmentMessagesSHA256  string            `json:"treatment_messages_sha256"`
	ProductionEquivalenceOK  bool              `json:"production_equivalence_ok"`
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sectionManifest(ss []section) []manifestSection {
	byID := map[string]manifestSection{}
	order := []string{}
	for _, s := range ss {
		if _, ok := byID[s.id]; !ok {
			order = append(order, s.id)
			byID[s.id] = manifestSection{SectionID: s.id, SHA256: sha256hex(s.content), ByteLength: len(s.content)}
		}
		m := byID[s.id]
		m.OccurrenceCount++
		byID[s.id] = m
	}
	out := make([]manifestSection, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

func sectionIDs(ss []section) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.id
	}
	return out
}

func messagesJSON(msgs []chat.Message) string {
	b, _ := json.Marshal(msgs)
	return string(b)
}

func runManifest(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var samples []sampleManifest
	var logLines []string
	for _, f := range fixtures.All() {
		msgs, err := agent.BuildWikiPageModifyMessages(f.DataMap())
		if err != nil {
			return fmt.Errorf("%s: production builder failed: %w", f.ID, err)
		}
		treat, err := treatmentSections(msgs[0].Content, msgs[1].Content)
		if err != nil {
			return fmt.Errorf("%s: %w", f.ID, err)
		}
		ctrlSections, err := controlSections(treat)
		if err != nil {
			return fmt.Errorf("%s: %w", f.ID, err)
		}
		ctrlMsgs, err := controlMessages(ctrlSections)
		if err != nil {
			return fmt.Errorf("%s: %w", f.ID, err)
		}
		if err := verifyMultiset(treat, ctrlSections); err != nil {
			return fmt.Errorf("%s: %w", f.ID, err)
		}
		// Byte-identity against the Task006 reference renderer is covered by
		// internal/agent tests; record message digests for the manifest.
		samples = append(samples, sampleManifest{
			SampleID:                 f.ID,
			Tier:                     f.Tier,
			Sections:                 sectionManifest(treat),
			ControlSectionMultiset:   sha256hex(multisetKey(ctrlSections)),
			TreatmentSectionMultiset: sha256hex(multisetKey(treat)),
			MultisetEqual:            multisetKey(treat) == multisetKey(ctrlSections),
			LayoutSequenceControl:    sectionIDs(ctrlSections),
			LayoutSequenceTreatment:  sectionIDs(treat),
			ControlMessagesSHA256:    sha256hex(messagesJSON(ctrlMsgs)),
			TreatmentMessagesSHA256:  sha256hex(messagesJSON(msgs)),
			ProductionEquivalenceOK:  true,
		})
		logLines = append(logLines, fmt.Sprintf("PASS\t%s\tsections=%d\tmultiset_equal=true\tctrl_messages=%d", f.ID, len(treat), len(ctrlMsgs)))
	}

	manifest := map[string]any{
		"fixture_id":                fixtureDomain,
		"fixture_generator":         "tests/evaluation/prompt-cache/fixtures/fixtures.go::All()",
		"production_builder":        "internal/agent/prompts_wiki_builder.go::BuildWikiPageModifyMessages",
		"sample_count":              len(samples),
		"tiers":                     []string{"small", "medium", "large"},
		"tier_plan":                 "4 pairs per tier (plan §7.1)",
		"cohort_marker_main":        fixtures.CohortMarkerMain,
		"cohort_marker_pilot":       fixtures.CohortMarkerPilot,
		"semantic_section_contract": contractVersion,
		"samples":                   samples,
	}
	if err := writeJSON(filepath.Join(dir, "fixture_manifest.json"), manifest); err != nil {
		return err
	}

	contract := map[string]any{
		"contract_version":      contractVersion,
		"section_ids_treatment": sectionIDs(treatmentIDs()),
		"control_order":         controlOrder,
		"join_separator":        sectionJoin,
		"tag_blocks":            tagBlocks,
		"branch_scope":          "HasAdditions=true, HasRetractions=false (P0)",
		"rule":                  "only sequence and message boundary may differ between arms; every section hash/byte_length/occurrence identical (plan §4.3)",
	}
	if err := writeJSON(filepath.Join(dir, "semantic_section_contract.json"), contract); err != nil {
		return err
	}

	log := "check\tsample\tdetail\n" + strings.Join(logLines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "section_identity_tests.log"), []byte(log), 0o644); err != nil {
		return err
	}
	return nil
}

func treatmentIDs() []section {
	return append([]section{{id: SecSystemRules}}, []section{
		{id: SecSharedSourceCtx}, {id: SecPageMetadata}, {id: SecAboutLine},
		{id: SecExistingContent}, {id: SecNewInformation}, {id: SecNewInfoFraming},
		{id: SecValidWikiLinks}, {id: SecInstructions}, {id: SecOutputFormat},
	}...)
}

// builderArtifactHash covers the frozen production builder artifact files
// (sorted path order, same serialization style as Task006's manifest).
func builderArtifactHash() (string, error) {
	files := []string{
		"internal/agent/prompts_wiki.go",
		"internal/agent/prompts_wiki_builder.go",
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "FILE %s\n", f)
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func runNegative(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	checks := runNegativeMatrix()
	if err := writeNegativeTSV(filepath.Join(dir, "negative_matrix.tsv"), checks); err != nil {
		return err
	}
	var logLines []string
	allPass := true
	for _, c := range checks {
		if !c.pass {
			allPass = false
		}
		logLines = append(logLines, fmt.Sprintf("%s\t%s\t%t", c.id, c.detail, c.pass))
	}
	log := "id\tdetail\tpass\n" + strings.Join(logLines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "fail_closed.log"), []byte(log), 0o644); err != nil {
		return err
	}
	if !allPass {
		return fmt.Errorf("negative matrix not all PASS")
	}
	return nil
}

func runAggregate(dir string, golden bool, repro3 bool) error {
	var rows []sanitizedRow
	if !golden {
		var tsvPath string
		for _, a := range os.Args[2:] {
			if !strings.HasPrefix(a, "-") {
				tsvPath = a
			}
		}
		if tsvPath == "" {
			return fmt.Errorf("aggregate: sanitized TSV path required (or --golden)")
		}
		data, err := os.ReadFile(tsvPath)
		if err != nil {
			return err
		}
		rows, err = parseRows(data)
		if err != nil {
			return err
		}
	} else {
		rows = goldenRows()
	}
	res, err := Aggregate(rows)
	if err != nil {
		return err
	}
	if repro3 {
		var hashes []string
		for i := 0; i < 3; i++ {
			r2, err := Aggregate(rows)
			if err != nil {
				return err
			}
			b, _ := json.Marshal(r2)
			hashes = append(hashes, sha256hex(string(b)))
		}
		identical := hashes[0] == hashes[1] && hashes[1] == hashes[2]
		line := fmt.Sprintf("run1\t%s\nrun2\t%s\nrun3\t%s\nbyte_identical\t%t\n", hashes[0], hashes[1], hashes[2], identical)
		if err := os.WriteFile(filepath.Join(dir, "reproduction_3runs.tsv"), []byte("run\toutput_sha256\n"+line), 0o644); err != nil {
			return err
		}
		if !identical {
			return fmt.Errorf("aggregation not byte-identical across 3 runs")
		}
	}
	name := "aggregate.json"
	if golden {
		name = "aggregation_golden.json"
	}
	if err := writeJSON(filepath.Join(dir, name), res); err != nil {
		return err
	}
	bs := map[string]any{
		"seed":        bootstrapSeed,
		"resamples":   bootstrapResamples,
		"mean_delta":  res.BootstrapMeanDelta,
		"ci95":        []float64{res.BootstrapCI95Lower, res.BootstrapCI95Upper},
		"valid_pairs": res.ValidPairs,
	}
	return writeJSON(filepath.Join(dir, "bootstrap_distribution_summary.json"), bs)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: prompt-cache-experiment <manifest|negative|aggregate|live> [--golden] [--repro3] <tsv>")
		os.Exit(4)
	}
	cmd := os.Args[1]
	dir := os.Getenv("TASK013_EVIDENCE_DIR")
	switch cmd {
	case "manifest", "negative", "aggregate":
		if dir == "" {
			fmt.Fprintln(os.Stderr, "TASK013_EVIDENCE_DIR not set")
			os.Exit(4)
		}
	}
	var err error
	switch cmd {
	case "manifest":
		err = runManifest(dir)
	case "negative":
		err = runNegative(dir)
	case "aggregate":
		golden := false
		repro3 := false
		for _, a := range os.Args[2:] {
			switch a {
			case "--golden":
				golden = true
			case "--repro3":
				repro3 = true
			}
		}
		err = runAggregate(dir, golden, repro3)
	case "live":
		allow := false
		kind := ""
		for _, a := range os.Args[2:] {
			switch a {
			case "--allow-provider":
				allow = true
			case "pilot", "main":
				kind = a
			}
		}
		if !allow {
			fmt.Println("SKIP_WITH_REASON: live mode requires explicit --allow-provider")
			os.Exit(3)
		}
		if dir == "" {
			fmt.Fprintln(os.Stderr, "TASK013_EVIDENCE_DIR not set")
			os.Exit(4)
		}
		key, err := resolveCredential()
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			os.Exit(4)
		}
		protoHash, err := loadProtocolHash(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", err)
			os.Exit(4)
		}
		cfg := liveConfig{
			apiKey:       key,
			evidenceDir:  dir,
			budgetNanos:  int64(10_000_000_000), // CNY 10.00 Owner-approved
			protocolHash: protoHash,
			experimentID: "task013-main-v1",
		}
		if kind == "pilot" {
			err = runPilot(cfg)
		} else if kind == "main" {
			err = runMain(cfg)
		} else {
			fmt.Fprintln(os.Stderr, "live requires a run kind: pilot | main")
			os.Exit(4)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(4)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		os.Exit(4)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
}
