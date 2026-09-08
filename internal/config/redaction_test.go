package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigShowOmitsEncryptionKeyMaterial(t *testing.T) {
	const marker = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("SYMBROWSE_ENCRYPTION_KEY", marker)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	result, err := LoadWithOverrides(FlagOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	jsonOutput, err := json.Marshal(ShowOutputFor(result))
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	if err := WriteShow(&text, result, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(jsonOutput), marker) || strings.Contains(text.String(), marker) {
		t.Fatal("config show exposed encryption key material")
	}
}
