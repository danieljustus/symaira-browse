// Command sourcecheck proves fixture inputs match a pinned Git revision.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
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
	root, err := os.OpenRoot(".")
	if err != nil {
		fatal("open repository root: %v", err)
	}
	defer root.Close()
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
			current, err := readRootFile(root, path)
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

func readRootFile(root *os.Root, path string) ([]byte, error) {
	file, err := root.Open(filepath.ToSlash(path))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source path is not a regular file")
	}
	return io.ReadAll(file)
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
	if path == "" || filepath.IsAbs(path) || isWindowsAbsolute(path) {
		return fmt.Errorf("source path must be non-empty and relative: %q", path)
	}
	// Check both separators so validation remains safe when a path is
	// supplied by a different host platform or passed to git on Windows.
	portable := strings.ReplaceAll(path, `\`, "/")
	clean := filepath.Clean(portable)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("source path escapes repository: %q", path)
	}
	return nil
}

func isWindowsAbsolute(path string) bool {
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\`) {
		return true
	}
	// Reject drive-relative paths (C:foo) as well as drive-rooted paths
	// (C:\\foo): neither is a repository-relative source path.
	return len(path) >= 2 && ((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z')) && path[1] == ':'
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
