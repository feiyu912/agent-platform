package retry

import (
	"math/rand"
	"testing"
	"time"
)

func TestBackoffNext(t *testing.T) {
	policy := BackoffPolicy{Min: time.Second, Max: 10 * time.Second, Factor: 2}
	if got := policy.Next(0); got != time.Second {
		t.Fatalf("initial backoff = %s, want 1s", got)
	}
	if got := policy.Next(2 * time.Second); got != 4*time.Second {
		t.Fatalf("next backoff = %s, want 4s", got)
	}
	if got := policy.Next(8 * time.Second); got != 10*time.Second {
		t.Fatalf("capped backoff = %s, want 10s", got)
	}
}

func TestBackoffNormalizesInvalidValues(t *testing.T) {
	policy := BackoffPolicy{}
	if got := policy.Next(0); got != time.Second {
		t.Fatalf("default initial backoff = %s, want 1s", got)
	}
	policy = BackoffPolicy{Min: 5 * time.Second, Max: time.Second, Factor: 1}
	if got := policy.Next(5 * time.Second); got != 5*time.Second {
		t.Fatalf("normalized capped backoff = %s, want 5s", got)
	}
}

func TestBackoffJitterRange(t *testing.T) {
	policy := BackoffPolicy{Min: time.Second, Max: 10 * time.Second, Factor: 2, JitterRatio: 0.2}
	rng := rand.New(rand.NewSource(1))
	base := 10 * time.Second
	for i := 0; i < 100; i++ {
		got := policy.Jitter(base, rng)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("jittered delay %s outside expected range", got)
		}
	}
}
