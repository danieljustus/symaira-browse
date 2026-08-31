package state

import (
	"errors"
	"strings"
	"testing"
)

type fakeKeychainExitError int

func (e fakeKeychainExitError) Error() string { return "security failed" }
func (e fakeKeychainExitError) ExitCode() int { return int(e) }

func TestKeychainLookupResultTreatsExit44AsNotFound(t *testing.T) {
	value, found, err := keychainLookupResult(nil, fakeKeychainExitError(44))
	if err != nil || found || value != nil {
		t.Fatalf("result = (%q, %t, %v), want (nil, false, nil)", value, found, err)
	}
}

func TestKeychainLookupResultPropagatesOtherFailures(t *testing.T) {
	for _, input := range []error{fakeKeychainExitError(1), errors.New("exec unavailable")} {
		value, found, err := keychainLookupResult(nil, input)
		if err == nil || found || value != nil {
			t.Fatalf("result for %v = (%q, %t, %v), want lookup error", input, value, found, err)
		}
		if !strings.Contains(err.Error(), "keychain lookup") {
			t.Fatalf("error = %v, want keychain context", err)
		}
	}
}
