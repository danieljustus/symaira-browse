package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestREADMEHelpCommandsAreRegistered(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	readme, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(readme), "\r\n", "\n")
	startMarker := "$ ./symbrowse --help\n"
	start := strings.Index(text, startMarker)
	if start < 0 {
		t.Fatal("README does not contain a captured symbrowse --help block")
	}
	start += len(startMarker)
	end := strings.Index(text[start:], "\n```")
	if end < 0 {
		t.Fatal("README help block is not closed")
	}
	help := text[start : start+end]
	commandLine := regexp.MustCompile(`^  ([a-z][a-z0-9-]*)\s{2,}`)
	root := newRootCommand()
	registered := make(map[string]bool)
	for _, command := range root.Commands() {
		registered[command.Name()] = true
	}
	seen := 0
	for _, line := range strings.Split(help, "\n") {
		match := commandLine.FindStringSubmatch(line)
		if len(match) != 2 {
			continue
		}
		if match[1] == "completion" || match[1] == "help" {
			continue
		}
		seen++
		if !registered[match[1]] {
			t.Errorf("README help advertises unregistered command %q", match[1])
		}
	}
	if seen == 0 {
		t.Fatal("README help block contains no command entries")
	}
}
