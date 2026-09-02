package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/updatecheck"
	"github.com/danieljustus/symaira-corekit/updatecheck/installmethod"

	"github.com/danieljustus/symaira-browse/internal/output"
)

func TestApplyUpgradeUsesExplicitHomebrewInstallMethod(t *testing.T) {
	originalExecutable := upgradeExecutable
	originalDetect := detectInstallMethod
	originalApply := applyRelease
	t.Cleanup(func() {
		upgradeExecutable = originalExecutable
		detectInstallMethod = originalDetect
		applyRelease = originalApply
	})

	upgradeExecutable = func() (string, error) { return "/tmp/symbrowse", nil }
	detectInstallMethod = func(string) (installmethod.InstallMethod, error) {
		return installmethod.Homebrew, nil
	}
	called := false
	applyRelease = func(context.Context, *updatecheck.Release, string) error {
		called = true
		return errors.New("brew failure")
	}

	command, buffer := newOutputCommand(t)
	if err := command.PersistentFlags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	if err := applyUpgrade(command, &updatecheck.Release{TagName: "v9.9.9"}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("Apply was called for a Homebrew installation")
	}
	if !strings.Contains(buffer.String(), `"install_method":"homebrew"`) || !strings.Contains(buffer.String(), `"applied":false`) {
		t.Fatalf("output = %q", buffer.String())
	}
}

func TestApplyUpgradeDoesNotDowngradeNonHomebrewErrorsContainingBrew(t *testing.T) {
	originalExecutable := upgradeExecutable
	originalDetect := detectInstallMethod
	originalApply := applyRelease
	t.Cleanup(func() {
		upgradeExecutable = originalExecutable
		detectInstallMethod = originalDetect
		applyRelease = originalApply
	})

	upgradeExecutable = func() (string, error) { return "/tmp/symbrowse", nil }
	detectInstallMethod = func(string) (installmethod.InstallMethod, error) {
		return installmethod.DirectDownload, nil
	}
	applyRelease = func(context.Context, *updatecheck.Release, string) error {
		return errors.New("download from brew mirror failed")
	}

	command, _ := newOutputCommand(t)
	err := applyUpgrade(command, &updatecheck.Release{TagName: "v9.9.9"})
	if err == nil || !strings.Contains(err.Error(), "download from brew mirror failed") {
		t.Fatalf("error = %v, want the real Apply failure", err)
	}
	if got := output.FromError(err).Code; got != string(output.CodeInternal) {
		t.Fatalf("error code = %q, want stable internal code", got)
	}
}
