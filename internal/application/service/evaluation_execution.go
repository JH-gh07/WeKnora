package service

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	defaultEvaluationWorkers   = 1
	defaultEvaluationLeaseTTL  = 60 * time.Second
	defaultEvaluationHeartbeat = 20 * time.Second
)

// EvaluationExecutionProfile freezes the scheduler inputs that affect
// performance and recovery. Values are explicit and enter protocol v2; they
// are never derived from GOMAXPROCS or the host implicitly.
type EvaluationExecutionProfile struct {
	Workers           int
	LeaseTTL          time.Duration
	HeartbeatInterval time.Duration
}

func evaluationExecutionProfileFromEnv() (EvaluationExecutionProfile, error) {
	profile := EvaluationExecutionProfile{
		Workers: defaultEvaluationWorkers, LeaseTTL: defaultEvaluationLeaseTTL,
		HeartbeatInterval: defaultEvaluationHeartbeat,
	}
	var err error
	if raw := os.Getenv("WEKNORA_EVALUATION_WORKERS"); raw != "" {
		profile.Workers, err = strconv.Atoi(raw)
		if err != nil {
			return EvaluationExecutionProfile{}, fmt.Errorf("WEKNORA_EVALUATION_WORKERS: %w", err)
		}
	}
	if raw := os.Getenv("WEKNORA_EVALUATION_LEASE_TTL"); raw != "" {
		profile.LeaseTTL, err = time.ParseDuration(raw)
		if err != nil {
			return EvaluationExecutionProfile{}, fmt.Errorf("WEKNORA_EVALUATION_LEASE_TTL: %w", err)
		}
	}
	if raw := os.Getenv("WEKNORA_EVALUATION_HEARTBEAT_INTERVAL"); raw != "" {
		profile.HeartbeatInterval, err = time.ParseDuration(raw)
		if err != nil {
			return EvaluationExecutionProfile{}, fmt.Errorf("WEKNORA_EVALUATION_HEARTBEAT_INTERVAL: %w", err)
		}
	}
	if profile.Workers < 1 || profile.Workers > 64 {
		return EvaluationExecutionProfile{}, fmt.Errorf("evaluation workers must be in [1,64]")
	}
	if profile.LeaseTTL < time.Second {
		return EvaluationExecutionProfile{}, fmt.Errorf("evaluation lease ttl must be at least 1s")
	}
	if profile.HeartbeatInterval <= 0 || profile.HeartbeatInterval*2 >= profile.LeaseTTL {
		return EvaluationExecutionProfile{}, fmt.Errorf("evaluation heartbeat must be positive and less than half the lease ttl")
	}
	return profile, nil
}
