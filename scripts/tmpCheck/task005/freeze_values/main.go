package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
)

func main() {
	cfg := embedding.Config{
		Source:                    types.ModelSourceRemote,
		BaseURL:                   "https://fixture.invalid/v1",
		ModelName:                 "fixture-embedding-model",
		Dimensions:                3,
		SupportsDimensionOverride: true,
		ModelID:                   "fixture-embedding-model",
		Provider:                  "fixture",
	}
	pid := embedding.ComputeProviderIdentity(cfg)
	fp := embedding.ComputeModelConfigFingerprint(cfg)

	// dataset_content_hash = SHA-256 over the exact deterministic workload text.
	const warmups, rounds, samples, itemCount = 5, 3, 30, 40
	h := sha256.New()
	for r := 1; r <= rounds; r++ {
		for b := 0; b < warmups+samples; b++ {
			for i := 0; i < itemCount; i++ {
				fmt.Fprintf(h, "round-%d-batch-%04d-item-%04d\n", r, b, i)
			}
		}
	}
	datasetHash := hex.EncodeToString(h.Sum(nil))

	// evaluator_artifact_identity = SHA-256 of the experiment harness source.
	src, err := os.ReadFile("scripts/tmpCheck/task005/experiment/main.go")
	if err != nil {
		panic(err)
	}
	esum := sha256.Sum256(src)
	evalID := hex.EncodeToString(esum[:])

	fmt.Printf("provider_identity=%s\n", pid)
	fmt.Printf("model_config_fingerprint=%s\n", fp)
	fmt.Printf("dataset_content_hash=%s\n", datasetHash)
	fmt.Printf("evaluator_artifact_identity=%s\n", evalID)
}
