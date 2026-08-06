package chrome

import (
	"context"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
)

func TestScreenshotWithOptionsSendsJPEGQuality(t *testing.T) {
	var gotParams map[string]any
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		gotParams = paramsOf(t, req)
		return okReply(t, `{"data":"iVBORw0KGgo="}`) // "PNG" bytes, undecodable is fine here
	})
	_, err := e.ScreenshotWithOptions(context.Background(), engine.Page{SessionID: "sess-1"}, engine.ScreenshotOptions{
		Format:  "jpeg",
		Quality: 85,
	})
	if err != nil {
		t.Fatalf("ScreenshotWithOptions: %v", err)
	}
	if gotParams["format"] != "jpeg" {
		t.Fatalf("format = %v, want jpeg", gotParams["format"])
	}
	if gotParams["quality"] != float64(85) {
		t.Fatalf("quality = %v, want 85", gotParams["quality"])
	}
	if gotParams["captureBeyondViewport"] != nil && gotParams["captureBeyondViewport"] == true {
		t.Fatalf("captureBeyondViewport should be unset for viewport capture")
	}
}

func TestScreenshotWithOptionsSendsClip(t *testing.T) {
	var gotParams map[string]any
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		gotParams = paramsOf(t, req)
		return okReply(t, `{"data":"iVBORw0KGgo="}`)
	})
	_, err := e.ScreenshotWithOptions(context.Background(), engine.Page{SessionID: "sess-1"}, engine.ScreenshotOptions{
		Clip: &engine.Clip{X: 10, Y: 20, Width: 300, Height: 150},
	})
	if err != nil {
		t.Fatalf("ScreenshotWithOptions: %v", err)
	}
	clip, ok := gotParams["clip"].(map[string]any)
	if !ok {
		t.Fatalf("clip = %v, want an object", gotParams["clip"])
	}
	if clip["x"] != float64(10) || clip["y"] != float64(20) || clip["width"] != float64(300) || clip["height"] != float64(150) {
		t.Fatalf("clip = %v", clip)
	}
}

func TestScreenshotWithOptionsFullPage(t *testing.T) {
	var gotParams map[string]any
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		gotParams = paramsOf(t, req)
		return okReply(t, `{"data":"iVBORw0KGgo="}`)
	})
	_, err := e.ScreenshotWithOptions(context.Background(), engine.Page{SessionID: "sess-1"}, engine.ScreenshotOptions{FullPage: true})
	if err != nil {
		t.Fatalf("ScreenshotWithOptions: %v", err)
	}
	if gotParams["captureBeyondViewport"] != true {
		t.Fatalf("captureBeyondViewport = %v, want true", gotParams["captureBeyondViewport"])
	}
}

func TestScreenshotWithOptionsRejectsUnsupportedFormat(t *testing.T) {
	e := scriptedEngine(t, func(req rpcRequest) scriptedReply {
		t.Errorf("scripted peer must not be called for an invalid format")
		return scriptedReply{}
	})
	_, err := e.ScreenshotWithOptions(context.Background(), engine.Page{SessionID: "sess-1"}, engine.ScreenshotOptions{Format: "webp"})
	if err == nil || !strings.Contains(err.Error(), `unsupported screenshot format "webp"`) {
		t.Fatalf("err = %v, want unsupported format error", err)
	}
}
