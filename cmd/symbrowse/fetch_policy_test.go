package main

import (
	"testing"

	"github.com/danieljustus/symaira-browse/internal/config"
)

func TestResolveDaemonPolicyReportsFetchSSRF(t *testing.T) {
	setTestHome(t, t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	command := newDaemonCommand()

	policy := resolveDaemonPolicy(command, cfg)
	if !policy.FetchSSRFEnabled {
		t.Fatal("fetch_ssrf_enabled must be true when private targets are not allowed")
	}

	if err := command.Flags().Set("allow-private", "true"); err != nil {
		t.Fatal(err)
	}
	policy = resolveDaemonPolicy(command, cfg)
	if policy.FetchSSRFEnabled {
		t.Fatal("fetch_ssrf_enabled must be false with --allow-private")
	}
}
