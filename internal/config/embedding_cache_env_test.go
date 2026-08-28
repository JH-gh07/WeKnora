package config

import "testing"

// TestApplyEmbeddingCacheEnvOverrides pins the rollout switch: the section is
// disabled by default (nil => empty struct => false), and only an explicit
// "true" (case-insensitive) enables it, so a stale shell variable cannot
// silently enable the cache for a future deployment.
func TestApplyEmbeddingCacheEnvOverrides(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		expected bool
	}{
		{"unset defaults to disabled", "", false},
		{"true enables", "true", true},
		{"TRUE case-insensitive enables", "TRUE", true},
		{"false stays disabled", "false", false},
		{"garbage stays disabled", "yes", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WEKNORA_EMBEDDING_CACHE_ENABLED", tc.value)
			cfg := &Config{}
			applyEmbeddingCacheEnvOverrides(cfg)
			if cfg.EmbeddingCache == nil {
				t.Fatal("EmbeddingCache should be initialized even when disabled")
			}
			if cfg.EmbeddingCache.Enabled != tc.expected {
				t.Fatalf("Enabled = %v, want %v", cfg.EmbeddingCache.Enabled, tc.expected)
			}
		})
	}
}
