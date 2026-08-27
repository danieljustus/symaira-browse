package engine

import (
	"errors"
	"testing"
)

func TestOptionalInterfaceNamesComplete(t *testing.T) {
	if len(OptionalInterfaceNames) != 19 {
		t.Fatalf("expected 19 optional interfaces, got %d: %v", len(OptionalInterfaceNames), OptionalInterfaceNames)
	}
	for i := 1; i < len(OptionalInterfaceNames); i++ {
		if OptionalInterfaceNames[i-1] >= OptionalInterfaceNames[i] {
			t.Fatalf("interface names must be sorted and unique: %q before %q", OptionalInterfaceNames[i-1], OptionalInterfaceNames[i])
		}
	}
}

func TestCapabilitiesForPartitionsInterfaces(t *testing.T) {
	caps := CapabilitiesFor("test-engine", "TabManager", "FileTransfer")
	if caps.Kind != "test-engine" {
		t.Fatalf("kind = %q, want test-engine", caps.Kind)
	}
	if len(caps.Interfaces) != 2 {
		t.Fatalf("interfaces = %v, want exactly TabManager+FileTransfer", caps.Interfaces)
	}
	if caps.Interfaces[0] != "FileTransfer" || caps.Interfaces[1] != "TabManager" {
		t.Fatalf("interfaces = %v, want [FileTransfer TabManager] (canonical order)", caps.Interfaces)
	}
	if len(caps.Unsupported) != len(OptionalInterfaceNames)-2 {
		t.Fatalf("unsupported = %d, want %d", len(caps.Unsupported), len(OptionalInterfaceNames)-2)
	}
	for _, name := range caps.Interfaces {
		if listContains(caps.Unsupported, name) {
			t.Fatalf("interface %q appears in both lists", name)
		}
	}
}

func TestUnsupportedOperationErrorIsTyped(t *testing.T) {
	err := UnsupportedOperation("static", "network.har")
	if err == nil {
		t.Fatal("UnsupportedOperation returned nil")
	}
	var typed *UnsupportedOperationError
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not an *UnsupportedOperationError", err)
	}
	if typed.Engine != "static" || typed.Operation != "network.har" {
		t.Fatalf("typed error = %+v, want engine=static operation=network.har", typed)
	}
	want := `engine "static" does not support network.har`
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	// It must NOT match a plain runtime error.
	runtimeErr := errors.New("connection reset")
	if errors.As(runtimeErr, &typed) {
		t.Fatal("plain error must not match UnsupportedOperationError")
	}
}

func listContains(list []string, needle string) bool {
	for _, item := range list {
		if item == needle {
			return true
		}
	}
	return false
}
