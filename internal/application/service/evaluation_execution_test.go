package service

import (
	"testing"
	"time"
)

func TestEvaluationExecutionProfileIsExplicitAndValidated(t *testing.T) {
	for _, key := range []string{"WEKNORA_EVALUATION_WORKERS", "WEKNORA_EVALUATION_LEASE_TTL", "WEKNORA_EVALUATION_HEARTBEAT_INTERVAL"} {
		t.Setenv(key, "")
	}
	profile, err := evaluationExecutionProfileFromEnv()
	if err != nil {
		t.Fatalf("default profile: %v", err)
	}
	if profile.Workers != 1 || profile.LeaseTTL != time.Minute || profile.HeartbeatInterval != 20*time.Second {
		t.Fatalf("unexpected safe defaults: %+v", profile)
	}

	t.Setenv("WEKNORA_EVALUATION_WORKERS", "4")
	t.Setenv("WEKNORA_EVALUATION_LEASE_TTL", "90s")
	t.Setenv("WEKNORA_EVALUATION_HEARTBEAT_INTERVAL", "30s")
	profile, err = evaluationExecutionProfileFromEnv()
	if err != nil {
		t.Fatalf("explicit profile: %v", err)
	}
	if profile.Workers != 4 || profile.LeaseTTL != 90*time.Second || profile.HeartbeatInterval != 30*time.Second {
		t.Fatalf("explicit values ignored: %+v", profile)
	}

	t.Setenv("WEKNORA_EVALUATION_HEARTBEAT_INTERVAL", "45s")
	if _, err := evaluationExecutionProfileFromEnv(); err == nil {
		t.Fatal("heartbeat at half the ttl must fail closed")
	}
}
