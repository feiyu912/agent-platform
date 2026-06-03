package retry

import (
	"math/rand"
	"time"
)

type BackoffPolicy struct {
	Min         time.Duration
	Max         time.Duration
	Factor      float64
	JitterRatio float64
}

func (p BackoffPolicy) normalized() BackoffPolicy {
	if p.Min <= 0 {
		p.Min = time.Second
	}
	if p.Max <= 0 || p.Max < p.Min {
		p.Max = p.Min
	}
	if p.Factor <= 1 {
		p.Factor = 2
	}
	if p.JitterRatio < 0 {
		p.JitterRatio = 0
	}
	return p
}

func (p BackoffPolicy) Next(current time.Duration) time.Duration {
	p = p.normalized()
	if current <= 0 {
		return p.Min
	}
	next := time.Duration(float64(current) * p.Factor)
	if next < p.Min {
		next = p.Min
	}
	if next > p.Max {
		next = p.Max
	}
	return next
}

func (p BackoffPolicy) Jitter(delay time.Duration, rng *rand.Rand) time.Duration {
	p = p.normalized()
	if delay <= 0 || p.JitterRatio <= 0 {
		return delay
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	delta := int64(float64(delay) * p.JitterRatio)
	if delta <= 0 {
		return delay
	}
	return delay + time.Duration(rng.Int63n(delta*2+1)-delta)
}
