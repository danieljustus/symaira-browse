package main

import (
	"errors"
	"testing"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-browse/internal/state"
)

type failingStateKeyProvider struct {
	err error
}

func (p failingStateKeyProvider) Key() ([]byte, state.KeySource, error) {
	return nil, state.KeySourceKeychain, p.err
}

func (p failingStateKeyProvider) Source() (state.KeySource, error) {
	return state.KeySourceKeychain, p.err
}

func TestNewStateStorePropagatesResolverError(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	t.Setenv("SYMBROWSE_STATE_DIR", "")
	expected := errors.New("keychain access denied")

	_, err := newStateStoreWithResolver(&cobra.Command{}, failingStateKeyProvider{err: expected})
	if !errors.Is(err, expected) {
		t.Fatalf("newStateStore error = %v, want %v", err, expected)
	}
}
