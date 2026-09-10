package main

import (
	"os"
	"path/filepath"
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
	for _, path := range []string{
		"",
		"../secret",
		`..\secret`,
		"/absolute",
		`\absolute`,
		`C:\absolute`,
		`C:relative`,
		`\\server\share\file`,
	} {
		if err := validatePath(path); err == nil {
			t.Fatalf("path %q accepted", path)
		}
	}
}

func TestValidatePathAcceptsRepositoryRelativePath(t *testing.T) {
	for _, path := range []string{"scripts/rust-port/cmd/sourcecheck/main.go", filepath.Join("scripts", "rust-port", "cmd", "sourcecheck", "main.go")} {
		if err := validatePath(path); err != nil {
			t.Fatalf("path %q rejected: %v", path, err)
		}
	}
}

func TestReadRootFileRejectsExternalSymlink(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "repo")
	outsideDir := filepath.Join(base, "outside")
	if err := os.Mkdir(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(outsideDir, "same.go")
	if err := os.WriteFile(outside, []byte("matching bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(rootDir, "same.go")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := readRootFile(root, "same.go"); err == nil {
		t.Fatal("external symlink was read as repository content")
	}
}
