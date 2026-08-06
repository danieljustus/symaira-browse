package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

// fakeScreenshotEngine implements the protocol-neutral engine plus the
// screenshot-options and inspection extensions used by the capture runtime
// tests. It records the last capture options and returns canned image bytes.
type fakeScreenshotEngine struct {
	lastOptions engine.ScreenshotOptions
	data        []byte
}

func (f *fakeScreenshotEngine) Launch(context.Context) error { return nil }
func (f *fakeScreenshotEngine) NewContext(context.Context) (engine.Context, error) {
	return engine.Context{ID: "ctx"}, nil
}
func (f *fakeScreenshotEngine) NewPage(context.Context, engine.Context, string) (engine.Page, error) {
	return engine.Page{ID: "page"}, nil
}
func (f *fakeScreenshotEngine) Navigate(context.Context, engine.Page, string) (engine.NavigationResult, error) {
	return engine.NavigationResult{}, nil
}
func (f *fakeScreenshotEngine) Evaluate(context.Context, engine.Page, string) (engine.EvaluationResult, error) {
	return engine.EvaluationResult{}, nil
}
func (f *fakeScreenshotEngine) AXTree(context.Context, engine.Page) ([]engine.AXNode, error) {
	return nil, nil
}
func (f *fakeScreenshotEngine) Screenshot(context.Context, engine.Page) ([]byte, error) {
	return f.data, nil
}
func (f *fakeScreenshotEngine) Close() error { return nil }

func (f *fakeScreenshotEngine) ScreenshotWithOptions(_ context.Context, _ engine.Page, opts engine.ScreenshotOptions) ([]byte, error) {
	f.lastOptions = opts
	return f.data, nil
}

func (f *fakeScreenshotEngine) Inspect(_ context.Context, _ engine.Page, request engine.InspectionRequest, _ *engine.InteractionTarget) (engine.InspectionResult, error) {
	if request.Kind == engine.InspectBox {
		return engine.InspectionResult{Kind: engine.InspectBox, Selector: request.Selector, Value: json.RawMessage(`{"x":5,"y":7,"width":120,"height":40}`)}, nil
	}
	return engine.InspectionResult{Kind: request.Kind}, nil
}

// newScreenshotRuntime wires a NavigationRuntime around a fake screenshot
// engine with the given allowed screenshot roots.
func newScreenshotRuntime(t *testing.T, screenshotDirs []string) (*NavigationRuntime, *fakeScreenshotEngine) {
	t.Helper()
	registry := NewSessionRegistry(SessionRegistryOptions{PID: 1})
	if _, err := registry.Ensure("default"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeScreenshotEngine{data: testPNG(t, 2, 3)}
	runtime := &NavigationRuntime{
		registry:       registry,
		executable:     "/fake/chrome",
		tabs:           make(map[string][]*sessionTab),
		activeTab:      make(map[string]int),
		engines:        make(map[string]engine.Engine),
		screenshotDirs: screenshotDirs,
	}
	service := engine.NewNavigationService(fake, engine.Page{ID: "page"}, engine.NavigationOptions{})
	runtime.tabs["default"] = []*sessionTab{{Label: "t1", Service: service, Page: engine.Page{ID: "page"}}}
	runtime.activeTab["default"] = 0
	runtime.engines["default"] = fake
	return runtime, fake
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func screenshotFrame(args map[string]any) Frame {
	raw, _ := json.Marshal(args)
	return Frame{Cmd: "screenshot", Session: "default", Args: raw}
}

func TestScreenshotFrameWritesDefaultCacheDir(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	runtime, fake := newScreenshotRuntime(t, []string{out})
	data, warnings, err := runtime.Handle(context.Background(), screenshotFrame(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	result, ok := data.(screenshotResult)
	if !ok {
		t.Fatalf("data = %#v, want screenshotResult", data)
	}
	if !filepath.IsAbs(result.Path) || !strings.HasPrefix(result.Path, out) {
		t.Fatalf("path = %q, want inside %q", result.Path, out)
	}
	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatalf("screenshot file missing: %v", err)
	}
	if info.Size() != int64(result.Bytes) {
		t.Fatalf("file size = %d, result bytes = %d", info.Size(), result.Bytes)
	}
	if result.Width != 2 || result.Height != 3 || result.Format != "png" {
		t.Fatalf("result = %+v", result)
	}
	if fake.lastOptions.Format != "" || fake.lastOptions.FullPage || fake.lastOptions.Clip != nil {
		t.Fatalf("capture options = %+v", fake.lastOptions)
	}
}

func TestScreenshotFramePathGuardRejectsOutsideRoot(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	other := t.TempDir()
	runtime, _ := newScreenshotRuntime(t, []string{out})
	_, _, err := runtime.Handle(context.Background(), screenshotFrame(map[string]any{"path": filepath.Join(other, "x.png")}))
	if err == nil || !strings.Contains(err.Error(), "outside the allowed screenshot directories") {
		t.Fatalf("err = %v, want path-guard rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(other, "x.png")); !os.IsNotExist(statErr) {
		t.Fatalf("guarded file was written anyway")
	}
}

func TestScreenshotFrameExplicitDirAllowsPath(t *testing.T) {
	dir := t.TempDir()
	runtime, _ := newScreenshotRuntime(t, []string{filepath.Join(t.TempDir(), "out")})
	target := filepath.Join(dir, "shots", "custom.png")
	data, _, err := runtime.Handle(context.Background(), screenshotFrame(map[string]any{"dir": dir, "path": target}))
	if err != nil {
		t.Fatal(err)
	}
	result := data.(screenshotResult)
	if result.Path != target {
		t.Fatalf("path = %q, want %q", result.Path, target)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("screenshot file missing: %v", statErr)
	}
}

func TestScreenshotFrameElementClip(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	runtime, fake := newScreenshotRuntime(t, []string{out})
	_, _, err := runtime.Handle(context.Background(), screenshotFrame(map[string]any{"selector": "button"}))
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastOptions.Clip == nil {
		t.Fatal("expected a clip for the element screenshot")
	}
	clip := fake.lastOptions.Clip
	if clip.X != 5 || clip.Y != 7 || clip.Width != 120 || clip.Height != 40 {
		t.Fatalf("clip = %+v", clip)
	}
}

func TestScreenshotFrameFullPage(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	runtime, fake := newScreenshotRuntime(t, []string{out})
	_, _, err := runtime.Handle(context.Background(), screenshotFrame(map[string]any{"full": true}))
	if err != nil {
		t.Fatal(err)
	}
	if !fake.lastOptions.FullPage {
		t.Fatalf("FullPage not set: %+v", fake.lastOptions)
	}
}

func TestScreenshotFrameFullAndSelectorAreMutuallyExclusive(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	runtime, _ := newScreenshotRuntime(t, []string{out})
	_, _, err := runtime.Handle(context.Background(), screenshotFrame(map[string]any{"full": true, "selector": "button"}))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want mutual-exclusion error", err)
	}
}

func TestScreenshotFrameRejectsUnsupportedFormat(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	runtime, _ := newScreenshotRuntime(t, []string{out})
	_, _, err := runtime.Handle(context.Background(), screenshotFrame(map[string]any{"format": "gif"}))
	if err == nil || !strings.Contains(err.Error(), `unsupported screenshot format "gif"`) {
		t.Fatalf("err = %v, want unsupported format error", err)
	}
}

func TestScreenshotFrameJPEGDimensions(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	runtime, fake := newScreenshotRuntime(t, []string{out})
	fake.data = testJPEG(t, 4, 5)
	data, _, err := runtime.Handle(context.Background(), screenshotFrame(map[string]any{"format": "jpeg", "quality": 70}))
	if err != nil {
		t.Fatal(err)
	}
	result := data.(screenshotResult)
	if result.Format != "jpeg" || result.Width != 4 || result.Height != 5 {
		t.Fatalf("result = %+v", result)
	}
	if fake.lastOptions.Format != "jpeg" || fake.lastOptions.Quality != 70 {
		t.Fatalf("capture options = %+v", fake.lastOptions)
	}
}

func TestScreenshotFrameRequiresAllowedDirectory(t *testing.T) {
	runtime, _ := newScreenshotRuntime(t, nil)
	_, _, err := runtime.Handle(context.Background(), screenshotFrame(nil))
	if err == nil || !strings.Contains(err.Error(), "no allowed screenshot directory") {
		t.Fatalf("err = %v, want missing-directory error", err)
	}
}
