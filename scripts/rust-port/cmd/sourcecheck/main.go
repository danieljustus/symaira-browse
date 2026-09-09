// Command sourcecheck proves fixture inputs match a pinned Git revision.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	oracle := flag.String("oracle", "", "pinned Git commit")
	paths := flag.String("paths", "", "comma-separated repository-relative source paths")
	flag.Parse()
	if *oracle == "" || *paths == "" {
		fatal("--oracle and --paths are required")
	}
	count := 0
	seen := make(map[string]struct{})
	for _, requested := range strings.Split(*paths, ",") {
		requested = strings.TrimSpace(requested)
		if err := validatePath(requested); err != nil {
			fatal("%v", err)
		}
		files, err := expandFiles(requested)
		if err != nil {
			fatal("expand %s: %v", requested, err)
		}
		for _, path := range files {
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			current, err := os.ReadFile(path) // #nosec G304 -- validated repository-relative operator input
			if err != nil {
				fatal("read current %s: %v", path, err)
			}
			command := exec.Command("git", "show", *oracle+":"+filepath.ToSlash(path)) // #nosec G204 -- argv is separated and path is validated
			pinned, err := command.Output()
			if err != nil {
				fatal("read pinned %s: %v", path, err)
			}
			if err := compareSource(path, current, pinned); err != nil {
				fatal("%v", err)
			}
			count++
		}
	}
	fmt.Printf("PASS pinned fixture sources (%d files at %s)\n", count, *oracle)
}

func expandFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{filepath.ToSlash(path)}, nil
	}
	var files []string
	err = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		files = append(files, filepath.ToSlash(current))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func validatePath(path string) error {
	if path == "" || filepath.IsAbs(path) {
		return fmt.Errorf("source path must be non-empty and relative: %q", path)
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("source path escapes repository: %q", path)
	}
	return nil
}

func compareSource(path string, current, pinned []byte) error {
	currentDigest := sha256.Sum256(current)
	pinnedDigest := sha256.Sum256(pinned)
	if currentDigest == pinnedDigest {
		return nil
	}
	return fmt.Errorf(
		"fixture source drift for %s: current_sha256=%s pinned_sha256=%s",
		path,
		hex.EncodeToString(currentDigest[:]),
		hex.EncodeToString(pinnedDigest[:]),
	)
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
