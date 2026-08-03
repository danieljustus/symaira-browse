package diff

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func solidPNG(t *testing.T, width, height int, fill color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, fill)
		}
	}
	path := filepath.Join(t.TempDir(), "img.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCompareIdenticalScreenshots(t *testing.T) {
	baseline := solidPNG(t, 20, 10, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	result, err := CompareScreenshots(baseline, baseline, Options{Threshold: 0.001})
	if err != nil {
		t.Fatalf("CompareScreenshots: %v", err)
	}
	if result.DifferentPx != 0 {
		t.Errorf("DifferentPx = %d, want 0", result.DifferentPx)
	}
	if !result.Passed {
		t.Errorf("identical screenshots failed the threshold: %+v", result)
	}
	if result.TotalPx != 200 {
		t.Errorf("TotalPx = %d, want 200", result.TotalPx)
	}
	if len(result.DiffImagePNG) == 0 {
		t.Error("diff image is empty")
	}
}

func TestCompareDifferentScreenshots(t *testing.T) {
	baseline := solidPNG(t, 10, 10, color.RGBA{R: 255, G: 0, B: 0, A: 255})
	// Fully different image (opposite colors everywhere).
	changed := solidPNG(t, 10, 10, color.RGBA{R: 0, G: 0, B: 255, A: 255})
	result, err := CompareScreenshots(baseline, changed, Options{Threshold: 0.01})
	if err != nil {
		t.Fatalf("CompareScreenshots: %v", err)
	}
	if result.DifferentPx != 100 {
		t.Errorf("DifferentPx = %d, want 100", result.DifferentPx)
	}
	if result.Deviation != 1.0 {
		t.Errorf("Deviation = %f, want 1.0", result.Deviation)
	}
	if result.Passed {
		t.Error("fully different screenshots passed the threshold")
	}
}

func TestCompareScreenshotDeviationAndCIExit(t *testing.T) {
	baseline := solidPNG(t, 100, 100, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	// One quadrant different -> 25% deviation.
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			if x < 50 && y < 50 {
				img.Set(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
			}
		}
	}
	path := filepath.Join(t.TempDir(), "quad.png")
	file, _ := os.Create(path)
	_ = png.Encode(file, img)
	_ = file.Close()
	changed, _ := os.ReadFile(path)

	// Threshold 10%: must fail (CI exit semantics).
	result, err := CompareScreenshots(baseline, changed, Options{Threshold: 0.10})
	if err != nil {
		t.Fatalf("CompareScreenshots: %v", err)
	}
	if result.Passed {
		t.Errorf("25%% deviation passed a 10%% threshold")
	}
	if result.DeviationPct < 24 || result.DeviationPct > 26 {
		t.Errorf("DeviationPct = %f, want ~25", result.DeviationPct)
	}
	// Threshold 50%: must pass.
	result, err = CompareScreenshots(baseline, changed, Options{Threshold: 0.50})
	if err != nil {
		t.Fatalf("CompareScreenshots: %v", err)
	}
	if !result.Passed {
		t.Errorf("25%% deviation failed a 50%% threshold")
	}
}

func TestCompareDimensionMismatch(t *testing.T) {
	baseline := solidPNG(t, 10, 10, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	other := solidPNG(t, 11, 10, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	if _, err := CompareScreenshots(baseline, other, Options{}); err == nil {
		t.Fatal("dimension mismatch was accepted")
	}
}

func TestCompareInvalidPNG(t *testing.T) {
	if _, err := CompareScreenshots([]byte("not a png"), solidPNG(t, 4, 4, color.RGBA{}), Options{}); err == nil {
		t.Fatal("invalid baseline PNG was accepted")
	}
}

func TestScreenshotResultJSON(t *testing.T) {
	result := &ScreenshotResult{Width: 1, Height: 1, DifferentPx: 0, TotalPx: 1, Deviation: 0, Threshold: 0.01, Passed: true}
	raw, err := result.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"deviation"`, `"passed"`, `"different_px"`} {
		if !contains(text, want) {
			t.Errorf("JSON lacks %s: %s", want, text)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
