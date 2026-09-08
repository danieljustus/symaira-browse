package main

import (
	"strings"
	"testing"
)

func TestCompareSourceNegativeControl(t *testing.T) {
	if err := compareSource("fixture.go", []byte("original"), []byte("original")); err != nil {
		t.Fatalf("equal source rejected: %v", err)
	}
	err := compareSource("fixture.go", []byte("mutated"), []byte("original"))
	if err == nil {
		t.Fatal("mutated source was accepted")
	}
	if strings.Contains(err.Error(), "mutated") || strings.Contains(err.Error(), "original") {
		t.Fatalf("source contents leaked in error: %v", err)
	}
}

func TestValidatePathRejectsEscape(t *testing.T) {
	for _, path := range []string{"", "../secret", "/absolute"} {
		if err := validatePath(path); err == nil {
			t.Fatalf("path %q accepted", path)
		}
	}
}
