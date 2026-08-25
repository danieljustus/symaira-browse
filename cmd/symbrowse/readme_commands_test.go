package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestREADMEHelpCommandsAreRegistered(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(readme)
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
