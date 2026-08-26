package formflow

import (
	"context"
	"sync"
	"time"
)

// DefaultMinHostInterval is the default respectful pacing between two
// automations against the same host (issue #281: a campaign across many
// brokers must not look like an attack).
const DefaultMinHostInterval = 5 * time.Second

// Pacer enforces a minimum interval between runs against the same host. It
// is deliberately per-host: campaigns across different brokers are not
// slowed down, only repeated hits on one host are.
type Pacer struct {
	minInterval time.Duration

	mu    sync.Mutex
	last  map[string]time.Time
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

// NewPacer returns a Pacer with the given minimum per-host interval. A
// non-positive interval selects DefaultMinHostInterval.
func NewPacer(minInterval time.Duration) *Pacer {
	if minInterval <= 0 {
		minInterval = DefaultMinHostInterval
	}
	return &Pacer{
		minInterval: minInterval,
		last:        make(map[string]time.Time),
		now:         time.Now,
		sleep: func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}
}

// Wait blocks until the host's pacing window allows the next run. The host
// key is recorded before sleeping so concurrent runs against one host
// serialize instead of stampeding.
func (p *Pacer) Wait(ctx context.Context, host string) error {
	p.mu.Lock()
	now := p.now()
	deadline := p.last[host].Add(p.minInterval)
	p.last[host] = now
	sleep := p.sleep
	if deadline.After(now) {
		p.last[host] = deadline
	}
	p.mu.Unlock()

	if !deadline.After(now) {
		return nil
	}
	return sleep(ctx, deadline.Sub(now))
}
