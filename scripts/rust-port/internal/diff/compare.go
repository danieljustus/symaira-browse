package diff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Compare checks the observable contract selected by testCase.
func Compare(testCase Case, left, right Result) error {
	if left.TimedOut != right.TimedOut {
		return fmt.Errorf("timeout mismatch: left=%t right=%t", left.TimedOut, right.TimedOut)
	}
	if left.ExitCode != right.ExitCode {
		return fmt.Errorf("exit mismatch: left=%d right=%d", left.ExitCode, right.ExitCode)
	}
	if left.Signal != right.Signal {
		return fmt.Errorf("signal mismatch: left=%q right=%q", left.Signal, right.Signal)
	}
	if err := compareStream("stdout", testCase.stdoutComparisonMode(), left.Stdout, right.Stdout, left.SandboxRoot, right.SandboxRoot, testCase.IgnoreJSONFields); err != nil {
		return err
	}
	if err := compareStream("stderr", testCase.stderrComparisonMode(), left.Stderr, right.Stderr, left.SandboxRoot, right.SandboxRoot, testCase.IgnoreJSONFields); err != nil {
		return err
	}
	if testCase.CompareFiles && !reflect.DeepEqual(left.Files, right.Files) {
		return fmt.Errorf("filesystem manifest mismatch: left=%s right=%s", digestValue(left.Files), digestValue(right.Files))
	}
	return nil
}

func compareStream(name, mode string, left, right []byte, leftRoot, rightRoot string, ignoredJSONFields []string) error {
	switch mode {
	case comparisonModeIgnore:
		return nil
	case comparisonModeBytes:
	case comparisonModeConsoleText:
		left = normalizeConsole(left, leftRoot)
		right = normalizeConsole(right, rightRoot)
	case comparisonModeJSON:
		return compareJSON(name, left, right, leftRoot, rightRoot, ignoredJSONFields)
	default:
		return fmt.Errorf("unsupported %s comparison mode %q", name, mode)
	}
	if !bytes.Equal(left, right) {
		return fmt.Errorf("%s mismatch: left_bytes=%d left_sha256=%s right_bytes=%d right_sha256=%s",
			name, len(left), digestBytes(left), len(right), digestBytes(right))
	}
	return nil
}

func compareJSON(name string, left, right []byte, leftRoot, rightRoot string, ignoredFields []string) error {
	var leftValue, rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return fmt.Errorf("decode left %s JSON: %w", name, err)
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return fmt.Errorf("decode right %s JSON: %w", name, err)
	}
	normalizeJSONStrings(leftValue, leftRoot)
	normalizeJSONStrings(rightValue, rightRoot)
	ignored := make(map[string]struct{}, len(ignoredFields))
	for _, field := range ignoredFields {
		ignored[field] = struct{}{}
	}
	removeJSONFields(leftValue, ignored)
	removeJSONFields(rightValue, ignored)
	if !reflect.DeepEqual(leftValue, rightValue) {
		return fmt.Errorf("%s JSON mismatch: left_sha256=%s right_sha256=%s", name, digestValue(leftValue), digestValue(rightValue))
	}
	return nil
}

func normalizeJSONStrings(value any, sandboxRoot string) {
	switch typed := value.(type) {
	case map[string]any:
		for field, child := range typed {
			if text, ok := child.(string); ok && sandboxRoot != "" {
				typed[field] = strings.ReplaceAll(text, sandboxRoot, "<SANDBOX>")
				continue
			}
			normalizeJSONStrings(child, sandboxRoot)
		}
	case []any:
		for index, child := range typed {
			if text, ok := child.(string); ok && sandboxRoot != "" {
				typed[index] = strings.ReplaceAll(text, sandboxRoot, "<SANDBOX>")
				continue
			}
			normalizeJSONStrings(child, sandboxRoot)
		}
	}
}

func removeJSONFields(value any, ignored map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for field, child := range typed {
			if _, ok := ignored[field]; ok {
				delete(typed, field)
				continue
			}
			removeJSONFields(child, ignored)
		}
	case []any:
		for _, child := range typed {
			removeJSONFields(child, ignored)
		}
	}
}

func normalizeConsole(value []byte, sandboxRoot string) []byte {
	value = bytes.ReplaceAll(value, []byte("\r\n"), []byte("\n"))
	if sandboxRoot != "" {
		value = bytes.ReplaceAll(value, []byte(sandboxRoot), []byte("<SANDBOX>"))
	}
	return value
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func digestValue(value any) string {
	return digestBytes([]byte(fmt.Sprintf("%#v", value)))
}
