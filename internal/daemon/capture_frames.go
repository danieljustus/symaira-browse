package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/injection"
)

// snapshotRequest models the snapshot frame payload, combining accessibility tree
// options with injection-scan controls (issue #192).
type snapshotRequest struct {
	engine.SnapshotOptions
	NoInjectionScan   bool   `json:"no_injection_scan,omitempty"`
	InjectionPatterns string `json:"injection_patterns,omitempty"`
}

// handleCaptureFrame serves the page capture commands: snapshot, a11y
// audit and screenshot.
func (r *NavigationRuntime) handleCaptureFrame(ctx context.Context, frame Frame) (any, []Warning, error) {
	service, err := r.service(ctx, frame.Session)
	if err != nil {
		return nil, nil, err
	}
	switch frame.Cmd {
	case "snapshot":
		var request snapshotRequest
		if err := decodeOptionalArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		var result any
		var documentVersion string
		if request.Diff || request.Since != "" {
			diffResult, err := service.SnapshotDiff(ctx, request.SnapshotOptions)
			if err != nil {
				return nil, nil, err
			}
			result = diffResult
			documentVersion = snapshotDocumentVersion(diffResult)
		} else {
			snapResult, err := service.Snapshot(ctx, request.SnapshotOptions)
			if err != nil {
				return nil, nil, err
			}
			result = snapResult
			documentVersion = snapshotDocumentVersion(snapResult)
		}
		var warnings []Warning
		if !request.NoInjectionScan {
			warnings = r.scanSnapshotInjection(ctx, service, request.InjectionPatterns, documentVersion)
		}
		return result, warnings, nil
	case "a11y":
		var options engine.A11yOptions
		if err := decodeOptionalArgs(frame, &options); err != nil {
			return nil, nil, err
		}
		result, err := service.Audit(ctx, options)
		if err != nil {
			return nil, nil, err
		}
		return result, nil, nil
	case "screenshot":
		var request screenshotRequest
		if err := decodeOptionalArgs(frame, &request); err != nil {
			return nil, nil, err
		}
		result, err := r.captureScreenshot(ctx, service, request)
		return result, nil, err
	default:
		return nil, nil, fmt.Errorf("unknown capture command %q", frame.Cmd)
	}
}

const (
	// maxInjectionScanHTMLBytes bounds hostile HTML handed to the parser. The
	// remainder is deliberately not scanned and is reported to the caller.
	maxInjectionScanHTMLBytes = 1 << 20
	maxInjectionScanEntries   = 128
)

// scanSnapshotInjection runs the heuristic prompt-injection scan over the page HTML.
// Results are memoized by tab/page and the accessibility-tree document version;
// navigation and DOM changes therefore produce a new key without explicit hooks.
func (r *NavigationRuntime) scanSnapshotInjection(ctx context.Context, service *engine.NavigationService, patternsFile, documentVersion string) []Warning {
	pageURL := ""
	if urlResult, err := service.Inspect(ctx, engine.InspectionRequest{Kind: engine.InspectURL}); err == nil {
		_ = json.Unmarshal(urlResult.Value, &pageURL)
	}
	key := service.Page().ID + "\x00" + pageURL + "\x00" + documentVersion + "\x00" + patternsFile
	r.injectionMu.Lock()
	if cached, ok := r.injectionCache[key]; ok {
		warnings := cloneWarnings(cached)
		r.injectionMu.Unlock()
		return warnings
	}
	r.injectionMu.Unlock()

	htmlResult, err := service.Inspect(ctx, engine.InspectionRequest{Kind: engine.InspectHTML})
	if err != nil {
		return []Warning{{Kind: "injection_scan", Severity: "warning", Message: "injection scan failed: " + err.Error()}}
	}
	var pageHTML string
	if err := json.Unmarshal(htmlResult.Value, &pageHTML); err != nil {
		return []Warning{{Kind: "injection_scan", Severity: "warning", Message: "injection scan failed: " + err.Error()}}
	}
	limited := len(pageHTML) > maxInjectionScanHTMLBytes
	if limited {
		pageHTML = pageHTML[:maxInjectionScanHTMLBytes]
		for len(pageHTML) > 0 && !utf8.ValidString(pageHTML) {
			pageHTML = pageHTML[:len(pageHTML)-1]
		}
	}
	scanWarnings, err := injection.Scan(pageHTML, injection.ScanOptions{PatternsFile: patternsFile})
	if err != nil {
		return []Warning{{Kind: "injection_scan", Severity: "warning", Message: "injection scan failed: " + err.Error()}}
	}
	warnings := make([]Warning, 0, len(scanWarnings))
	for _, warning := range scanWarnings {
		warnings = append(warnings, Warning{
			Kind:     warning.Kind,
			Severity: warning.Severity,
			Message:  injectionMessage(warning),
			Ref:      warning.Ref,
			Excerpt:  warning.Excerpt,
		})
	}
	if limited {
		warnings = append(warnings, Warning{
			Kind:     "injection_scan",
			Severity: "warning",
			Message:  fmt.Sprintf("injection scan limited to %d bytes; content beyond the cap was not scanned", maxInjectionScanHTMLBytes),
		})
	}
	r.injectionMu.Lock()
	if r.injectionCache == nil {
		r.injectionCache = make(map[string][]Warning)
	}
	if len(r.injectionCache) >= maxInjectionScanEntries {
		r.injectionCache = make(map[string][]Warning)
	}
	r.injectionCache[key] = cloneWarnings(warnings)
	r.injectionMu.Unlock()
	return warnings
}

func snapshotDocumentVersion(result any) string {
	if snapshot, ok := result.(engine.SnapshotResult); ok {
		return fmt.Sprintf("%x", sha256.Sum256([]byte(snapshot.Tree)))
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf("%T", result)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func cloneWarnings(warnings []Warning) []Warning {
	return append([]Warning(nil), warnings...)
}

// injectionMessage renders a human-readable message for one detection.
func injectionMessage(warning injection.ScanWarning) string {
	switch warning.Kind {
	case injection.KindHiddenText:
		return "hidden text detected on " + warning.Ref
	case injection.KindImperative:
		return "agent-directed instruction detected on " + warning.Ref
	case injection.KindAriaMismatch:
		return "accessible-name mismatch on " + warning.Ref
	case injection.KindAttribute:
		return "instruction hidden in an attribute on " + warning.Ref
	case injection.KindComment:
		return "instruction hidden in an HTML comment"
	case injection.KindMeta:
		return "instruction hidden in meta content"
	}
	return "prompt-injection heuristic warning on " + warning.Ref
}

// screenshotRequest mirrors the daemon screenshot frame payload (issue #16,
// B-12): an optional target path, full-page or element capture, and the
// image format/quality. Paths are written only into the allowed screenshot
// roots (cache out dir by default, --screenshot-dir expands them).
type screenshotRequest struct {
	Path     string `json:"path,omitempty"`
	Full     bool   `json:"full,omitempty"`
	Selector string `json:"selector,omitempty"`
	Format   string `json:"format,omitempty"`
	Quality  int    `json:"quality,omitempty"`
	Dir      string `json:"dir,omitempty"`
}

// screenshotResult is the stable JSON shape returned for a capture: the file
// path, pixel dimensions, byte size and image format.
type screenshotResult struct {
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	Format string `json:"format"`
}

// captureScreenshot captures the page (optionally clipped to an element
// bounding box), writes it under an allowed screenshot root and returns the
// file path, dimensions and byte size.
func (r *NavigationRuntime) captureScreenshot(ctx context.Context, service *engine.NavigationService, request screenshotRequest) (any, error) {
	if request.Full && request.Selector != "" {
		return nil, errors.New("screenshot: --full and --selector are mutually exclusive")
	}
	// Validate and canonicalise the format for the file extension and result
	// field; the engine receives "" for an unset format (defaults to png,
	// and keeps the plain capability path for zero-option captures).
	resultFormat := normalizeScreenshotFormat(request.Format)
	if resultFormat == "" {
		return nil, fmt.Errorf("unsupported screenshot format %q (want png or jpeg)", request.Format)
	}
	engineFormat := ""
	if strings.EqualFold(request.Format, "jpeg") || strings.EqualFold(request.Format, "jpg") {
		engineFormat = "jpeg"
	}
	opts := engine.ScreenshotOptions{Format: engineFormat, Quality: request.Quality, FullPage: request.Full}
	if request.Selector != "" {
		// Element capture crops to the resolved bounding box (get.box path,
		// including @ref resolution and stale-ref handling).
		box, err := service.Inspect(ctx, engine.InspectionRequest{Kind: engine.InspectBox, Selector: request.Selector})
		if err != nil {
			return nil, err
		}
		var rect struct {
			X      float64 `json:"x"`
			Y      float64 `json:"y"`
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		}
		if err := json.Unmarshal(box.Value, &rect); err != nil {
			return nil, fmt.Errorf("decode element box for screenshot: %w", err)
		}
		opts.Clip = &engine.Clip{X: rect.X, Y: rect.Y, Width: rect.Width, Height: rect.Height}
	}
	data, err := service.ScreenshotWithOptions(ctx, opts)
	if err != nil {
		return nil, err
	}
	target, allowed, err := r.resolveScreenshotTarget(request, resultFormat)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return nil, fmt.Errorf("create screenshot directory: %w", err)
	}
	// Re-check the created directory: a symlinked path component could have
	// escaped the allowed root between the check and the write.
	if err := r.guardScreenshotTarget(target, allowed); err != nil {
		return nil, err
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		return nil, fmt.Errorf("write screenshot: %w", err)
	}
	width, height, err := imageDimensions(data)
	if err != nil {
		return nil, fmt.Errorf("decode screenshot dimensions: %w", err)
	}
	return screenshotResult{Path: target, Width: width, Height: height, Bytes: len(data), Format: resultFormat}, nil
}

// normalizeScreenshotFormat canonicalises the requested format: "" and "png"
// map to png, "jpg" to jpeg. Any other value returns "".
func normalizeScreenshotFormat(format string) string {
	switch strings.ToLower(format) {
	case "", "png":
		return "png"
	case "jpeg", "jpg":
		return "jpeg"
	default:
		return ""
	}
}

// resolveScreenshotTarget picks the target file: the explicit path when
// given, otherwise a timestamped name in the first allowed root. The
// --screenshot-dir request field expands the allowed roots (path guard,
// issue #16 AC): without it only the configured cache out dir is writable.
// It returns the absolute target and the full allowed root list.
func (r *NavigationRuntime) resolveScreenshotTarget(request screenshotRequest, format string) (string, []string, error) {
	allowed := r.screenshotDirs
	if len(allowed) == 0 {
		return "", nil, errors.New("screenshot: no allowed screenshot directory is configured")
	}
	if request.Dir != "" {
		allowed = append([]string{request.Dir}, allowed...)
	}
	target := request.Path
	if target == "" {
		target = filepath.Join(allowed[0], fmt.Sprintf("screenshot-%d.%s", time.Now().UnixNano(), format))
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", nil, fmt.Errorf("resolve screenshot path: %w", err)
	}
	// Resolve the deepest existing ancestor so symlinked prefixes (e.g.
	// /var -> /private/var on macOS) do not break the containment check;
	// the not-yet-existing remainder is appended unchanged.
	resolvedTarget := absolute
	if evaluated, evalErr := evalExistingAncestors(absolute); evalErr == nil {
		resolvedTarget = evaluated
	}
	for _, dir := range allowed {
		root, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if evaluated, evalErr := evalExistingAncestors(root); evalErr == nil {
			root = evaluated
		}
		if pathWithin(root, resolvedTarget) {
			return absolute, allowed, nil
		}
	}
	return "", nil, fmt.Errorf("screenshot path %q is outside the allowed screenshot directories (use --screenshot-dir to allow another directory)", target)
}

// evalExistingAncestors resolves the deepest existing ancestor of path with
// EvalSymlinks and re-appends the remaining components.
func evalExistingAncestors(path string) (string, error) {
	current := path
	for {
		evaluated, err := filepath.EvalSymlinks(current)
		if err == nil {
			rest, relErr := filepath.Rel(current, path)
			if relErr != nil {
				return evaluated, nil
			}
			return filepath.Join(evaluated, rest), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		current = parent
	}
}

// guardScreenshotTarget re-validates containment after directory creation so
// symlinked path components cannot redirect the write outside the roots.
func (r *NavigationRuntime) guardScreenshotTarget(target string, allowed []string) error {
	parent, err := filepath.Abs(filepath.Dir(target))
	if err != nil {
		return fmt.Errorf("resolve screenshot directory: %w", err)
	}
	evaluated, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("evaluate screenshot directory: %w", err)
	}
	for _, dir := range allowed {
		root, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if resolved, evalErr := evalExistingAncestors(root); evalErr == nil {
			root = resolved
		}
		if pathWithin(root, evaluated) {
			return nil
		}
	}
	return fmt.Errorf("screenshot directory %q escaped the allowed screenshot directories", parent)
}

// pathWithin reports whether path is inside dir (or equals it).
func pathWithin(dir, path string) bool {
	relative, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// imageDimensions decodes the pixel size from PNG or JPEG bytes.
func imageDimensions(data []byte) (width, height int, err error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0, err
	}
	return config.Width, config.Height, nil
}
