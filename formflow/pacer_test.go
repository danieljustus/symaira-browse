package formflow

import (
	"context"
	"testing"
	"time"
)

func TestPacerEnforcesPerHostInterval(t *testing.T) {
	pacer := NewPacer(time.Second)
	now := time.Unix(1_700_000_000, 0)
	pacer.now = func() time.Time { return now }
	var slept []time.Duration
	pacer.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}

	ctx := context.Background()
	if err := pacer.Wait(ctx, "a.example"); err != nil {
		t.Fatalf("first wait: %v", err)
	}
	if len(slept) != 0 {
		t.Fatalf("first contact with a host must not sleep, slept %v", slept)
	}

	now = now.Add(400 * time.Millisecond)
	if err := pacer.Wait(ctx, "a.example"); err != nil {
		t.Fatalf("second wait: %v", err)
	}
	if len(slept) != 1 || slept[0] != 600*time.Millisecond {
		t.Fatalf("second contact must sleep the remaining 600ms, slept %v", slept)
	}

	// A different host is unaffected: campaigns across brokers keep pace.
	if err := pacer.Wait(ctx, "b.example"); err != nil {
		t.Fatalf("other host wait: %v", err)
	}
	if len(slept) != 1 {
		t.Fatalf("different host must not sleep, slept %v", slept)
	}

	// After the interval elapsed, no sleep again.
	now = now.Add(2 * time.Second)
	if err := pacer.Wait(ctx, "a.example"); err != nil {
		t.Fatalf("later wait: %v", err)
	}
	if len(slept) != 1 {
		t.Fatalf("elapsed interval must not sleep, slept %v", slept)
	}
}

func TestPacerSerializesConcurrentHosts(t *testing.T) {
	// Two runs against the same host back-to-back serialize: the second one
	// sleeps the full interval, not just the remainder of the first.
	pacer := NewPacer(time.Second)
	now := time.Unix(1_700_000_000, 0)
	pacer.now = func() time.Time { return now }
	var slept []time.Duration
	pacer.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}

	ctx := context.Background()
	_ = pacer.Wait(ctx, "a.example")
	_ = pacer.Wait(ctx, "a.example")
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("back-to-back same-host runs must sleep the full interval, slept %v", slept)
	}
}

func TestHostOf(t *testing.T) {
	if got := hostOf("https://www.broker.example:8443/optout?x=1"); got != "www.broker.example:8443" {
		t.Fatalf("hostOf = %q", got)
	}
	if got := hostOf("not a url"); got != "not a url" {
		t.Fatalf("hostOf fallback = %q", got)
	}
}
