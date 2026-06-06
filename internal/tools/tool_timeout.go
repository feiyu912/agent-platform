package tools

import (
	"time"

	"agent-platform/internal/contracts"
)

func maxInt(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func toolTimeout(policy contracts.RetryPolicy) time.Duration {
	return policy.TimeoutDuration()
}
