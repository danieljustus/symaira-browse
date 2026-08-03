package axe

import (
	"strings"
	"testing"
)

func TestSourceEmbeddedAndVersioned(t *testing.T) {
	if Source() == "" {
		t.Fatal("axe-core source is empty")
	}
	if !strings.Contains(Source(), "axe") {
		t.Error("embedded source does not look like axe-core")
	}
	if Version != "4.10.2" {
		t.Errorf("Version = %s, want 4.10.2", Version)
	}
	// The vendored bundle must carry its version marker (axe.version).
	if !strings.Contains(Source(), "4.10.2") {
		t.Error("embedded bundle does not contain its version marker")
	}
}

func TestRunScriptShape(t *testing.T) {
	script, err := RunScript([]string{"wcag2a", "wcag2aa"}, "")
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	for _, want := range []string{"window.axe.run", "atob(", "wcag2a", "axe_version"} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
}

func TestRunScriptSelector(t *testing.T) {
	script, err := RunScript(nil, "#main")
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if !strings.Contains(script, "#main") {
		t.Errorf("script lacks selector: %.200s", script)
	}
}

func TestRunScriptNoTags(t *testing.T) {
	script, err := RunScript(nil, "")
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}
	if strings.Contains(script, "runOnly") {
		t.Errorf("script contains runOnly without tags: %.300s", script)
	}
}

func TestEncodeBase64(t *testing.T) {
	encoded := encodeBase64([]byte("Hello, 世界"))
	if encoded == "" {
		t.Fatal("empty encoding")
	}
	// Spot-check the alphabet contains only base64 characters.
	for _, char := range encoded {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=", char) {
			t.Fatalf("invalid base64 character %q", char)
		}
	}
}
