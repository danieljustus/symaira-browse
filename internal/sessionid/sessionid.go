// Package sessionid derives stable, collision-free session identifiers from
// the local repository layout (issue B-37). The scope determines what the ID
// is anchored to: a git worktree, the repository, or the current working
// directory.
package sessionid

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Scope identifies the anchor of a session id.
type Scope string

const (
	ScopeWorktree Scope = "worktree"
	ScopeRepo     Scope = "repo"
	ScopeCWD      Scope = "cwd"
)

// ValidScope reports whether s is a supported scope.
func ValidScope(s Scope) bool {
	switch s {
	case ScopeWorktree, ScopeRepo, ScopeCWD:
		return true
	default:
		return false
	}
}

// Scopes lists the supported scopes in stable order.
func Scopes() []Scope { return []Scope{ScopeWorktree, ScopeRepo, ScopeCWD} }

// Info is the stable payload returned by `session id`.
type Info struct {
	ID         string `json:"id"`
	Scope      Scope  `json:"scope"`
	Prefix     string `json:"prefix,omitempty"`
	OriginPath string `json:"origin_path"`
	Fallback   bool   `json:"fallback,omitempty"`
}

// ID derives a stable session identifier. Worktree and repo scopes resolve
// the git layout; cwd falls back to the working directory when the current
// directory is not inside a git repository.
func ID(scope Scope, prefix string, dir string) (Info, error) {
	if !ValidScope(scope) {
		return Info{}, fmt.Errorf("invalid scope %q", scope)
	}
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return Info{}, fmt.Errorf("determine working directory: %w", err)
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Info{}, fmt.Errorf("resolve absolute path: %w", err)
	}

	switch scope {
	case ScopeCWD:
		return Info{ID: applyPrefix(prefix, shortHash(dir)), Scope: scope, Prefix: prefix, OriginPath: dir, Fallback: true}, nil
	case ScopeWorktree, ScopeRepo:
		repo, worktree, err := resolveGitLayout(dir)
		if err != nil {
			// Outside a git repository: documented fallback to the cwd.
			return Info{ID: applyPrefix(prefix, shortHash(dir)), Scope: scope, Prefix: prefix, OriginPath: dir, Fallback: true}, nil
		}
		if scope == ScopeRepo {
			return Info{ID: applyPrefix(prefix, shortHash(repo)), Scope: scope, Prefix: prefix, OriginPath: repo}, nil
		}
		origin := worktree
		if origin == "" {
			origin = repo
		}
		return Info{ID: applyPrefix(prefix, shortHash(origin)), Scope: scope, Prefix: prefix, OriginPath: origin}, nil
	}
	return Info{}, errors.New("unreachable")
}

// applyPrefix prepends a validated prefix to a short hash.
func applyPrefix(prefix, hash string) string {
	if prefix == "" {
		return hash
	}
	return prefix + "-" + hash
}

// resolveGitLayout returns the absolute repository root and the worktree root
// for a directory. `git rev-parse --show-toplevel` already resolves linked
// worktrees to their own root and the main checkout to the repo root; the
// repository root is derived from `--git-common-dir`.
func resolveGitLayout(dir string) (repo, worktree string, err error) {
	toplevel, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	worktree = filepath.Clean(toplevel)
	commonDir, err := gitOutput(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return worktree, worktree, nil
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktree, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	// <repo>/.git -> <repo>; a bare repo stays as-is.
	if filepath.Base(commonDir) == ".git" {
		repo = filepath.Dir(commonDir)
	} else {
		repo = commonDir
	}
	return repo, worktree, nil
}

// gitOutput runs git in dir and returns trimmed stdout.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// shortHash returns the first 16 hex chars of the SHA-256 of value.
func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}
