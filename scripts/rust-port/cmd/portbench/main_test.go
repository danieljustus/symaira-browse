package main

import "testing"

func TestPercentileUsesNearestRank(t *testing.T) {
	values := make([]int64, 30)
	for i := range values {
		values[i] = int64(i + 1)
	}
	if got := percentile(values, 50); got != 15 {
		t.Fatalf("median = %d, want 15", got)
	}
	if got := percentile(values, 95); got != 29 {
		t.Fatalf("p95 = %d, want 29", got)
	}
}

func TestBenchmarkEnvIsIsolated(t *testing.T) {
	env := benchmarkEnv("/isolated/home", "/isolated/tmp")
	for _, expected := range []string{
		"HOME=/isolated/home",
		"XDG_CONFIG_HOME=/isolated/home/.config",
		"TZ=UTC",
		"SYMBROWSE_CHECK_UPDATES=0",
		"SYMBROWSE_SYMGUARD=off",
	} {
		found := false
		for _, value := range env {
			if value == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %q in %#v", expected, env)
		}
	}
}
