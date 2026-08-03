// Package diff provides pixel-level screenshot comparison and snapshot diff
// helpers for `symbrowse diff`.
package diff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// ScreenshotResult is the stable output of a screenshot comparison.
type ScreenshotResult struct {
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	DifferentPx  int     `json:"different_px"`
	TotalPx      int     `json:"total_px"`
	Deviation    float64 `json:"deviation"` // fraction of different pixels, 0..1
	DeviationPct float64 `json:"deviation_pct"`
	Threshold    float64 `json:"threshold"`
	Passed       bool    `json:"passed"`
	DiffImagePNG []byte  `json:"-"`
}

// Options controls a screenshot comparison.
type Options struct {
	// Threshold is the allowed deviation fraction (0..1); above it the
	// comparison fails (CI-exit-code semantics).
	Threshold float64
}

// DecodePNG decodes a PNG screenshot.
func DecodePNG(data []byte) (image.Image, error) {
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode png: %w", err)
	}
	return decoded, nil
}

// CompareScreenshots compares two PNG images pixel by pixel and produces a
// diff image highlighting differing pixels. Images must share dimensions.
func CompareScreenshots(baseline, current []byte, options Options) (*ScreenshotResult, error) {
	baseImage, err := DecodePNG(baseline)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	currentImage, err := DecodePNG(current)
	if err != nil {
		return nil, fmt.Errorf("current: %w", err)
	}
	bounds := baseImage.Bounds()
	if currentImage.Bounds() != bounds {
		return nil, fmt.Errorf("dimension mismatch: baseline %v vs current %v", bounds, currentImage.Bounds())
	}
	threshold := options.Threshold
	if threshold <= 0 {
		threshold = 0.001
	}

	result := &ScreenshotResult{
		Width:     bounds.Dx(),
		Height:    bounds.Dy(),
		Threshold: threshold,
	}
	diffImage := image.NewRGBA(bounds)
	total := bounds.Dx() * bounds.Dy()
	var different int
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			base := baseImage.At(x, y)
			current := currentImage.At(x, y)
			if !samePixel(base, current) {
				different++
				// Highlight differing pixels in magenta; keep the rest as a
				// dimmed copy so the diff image is self-explanatory.
				diffImage.Set(x, y, color.RGBA{R: 255, G: 0, B: 255, A: 255})
			} else {
				diffImage.Set(x, y, dim(base))
			}
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, diffImage); err != nil {
		return nil, fmt.Errorf("encode diff image: %w", err)
	}
	result.DifferentPx = different
	result.TotalPx = total
	if total > 0 {
		result.Deviation = float64(different) / float64(total)
		result.DeviationPct = result.Deviation * 100
	}
	result.Passed = result.Deviation <= threshold
	result.DiffImagePNG = buffer.Bytes()
	return result, nil
}

// samePixel compares two colors with tolerance for anti-aliasing.
func samePixel(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	const tolerance = 8000 // ~3% per channel in 16-bit space
	dr := absDiff(ar, br)
	dg := absDiff(ag, bg)
	db := absDiff(ab, bb)
	da := absDiff(aa, ba)
	return dr <= tolerance && dg <= tolerance && db <= tolerance && da <= tolerance
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

// dim darkens a color to keep the diff image readable.
func dim(c color.Color) color.Color {
	r, g, b, a := c.RGBA()
	return color.RGBA{R: uint8(r >> 10), G: uint8(g >> 10), B: uint8(b >> 10), A: uint8(a >> 8)}
}

// JSON renders the machine-readable comparison payload.
func (r *ScreenshotResult) JSON() ([]byte, error) {
	type payload struct {
		Width        int     `json:"width"`
		Height       int     `json:"height"`
		DifferentPx  int     `json:"different_px"`
		TotalPx      int     `json:"total_px"`
		Deviation    float64 `json:"deviation"`
		DeviationPct float64 `json:"deviation_pct"`
		Threshold    float64 `json:"threshold"`
		Passed       bool    `json:"passed"`
	}
	return json.Marshal(payload{
		Width: r.Width, Height: r.Height, DifferentPx: r.DifferentPx, TotalPx: r.TotalPx,
		Deviation: r.Deviation, DeviationPct: r.DeviationPct, Threshold: r.Threshold, Passed: r.Passed,
	})
}
