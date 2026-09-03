package safaribidi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/engine"
	"github.com/danieljustus/symaira-browse/internal/policy"
	"github.com/danieljustus/symaira-browse/internal/testserver"
)

// liveEngine launches a real safari-bidi engine, or skips.
//
// The engine needs no Safari running for normal browsing: safaridriver would
// otherwise attach to that instance and Safari would reject the session. See
// checkSafariBidiPrerequisites in internal/engine/doctor.
func liveEngine(t *testing.T) (*Engine, *testserver.Server) {
	t.Helper()
	if os.Getenv("SYMBROWSE_SAFARI_BIDI") == "" {
		t.Skip("set SYMBROWSE_SAFARI_BIDI=1 to run against a real Safari")
	}
	server := testserver.New(t)
	subject := New()
	if err := subject.Launch(context.Background()); err != nil {
		t.Fatalf("launch safari-bidi: %v", err)
	}
	t.Cleanup(func() { _ = subject.Close() })
	return subject, server
}

func livePage(t *testing.T, subject *Engine, url string) engine.Page {
	t.Helper()
	page, err := subject.NewPage(context.Background(), engine.Context{}, "")
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	if _, err := subject.Navigate(context.Background(), page, url); err != nil {
		t.Fatalf("navigate %s: %v", url, err)
	}
	return page
}

// TestLiveReadPath exercises the read path against the shared fixtures.
func TestLiveReadPath(t *testing.T) {
	subject, server := liveEngine(t)
	ctx := context.Background()
	page := livePage(t, subject, server.BaseURL+"/static")

	title, err := subject.Inspect(ctx, page, engine.InspectionRequest{Kind: engine.InspectTitle}, nil)
	if err != nil {
		t.Fatalf("inspect title: %v", err)
	}
	var titleValue string
	_ = json.Unmarshal(title.Value, &titleValue)
	if titleValue == "" {
		t.Error("title must not be empty on /static")
	}

	state, err := subject.NavigationState(ctx, page)
	if err != nil {
		t.Fatalf("navigation state: %v", err)
	}
	if !strings.HasSuffix(state.URL, "/static") {
		t.Errorf("url = %q, want the /static fixture", state.URL)
	}
	if state.ReadyState != "complete" {
		t.Errorf("ready state = %q, want complete; browsingContext.navigate wait=complete is meant to be a real load barrier", state.ReadyState)
	}
	// Measured limits, asserted so a future Safari that fixes them fails this
	// test loudly instead of silently improving behind an unchanged matrix.
	if state.HTTPStatus != 0 {
		t.Errorf("HTTPStatus = %d: Safari gained a network module; re-measure and revisit NetworkEvents", state.HTTPStatus)
	}

	nodes, err := subject.AXTree(ctx, page)
	if err != nil {
		t.Fatalf("ax tree: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("synthesized accessibility tree is empty")
	}
}

// TestLiveHydratedDOM is the read-path case that distinguishes this engine
// from static: /spa renders only after its script runs.
func TestLiveHydratedDOM(t *testing.T) {
	subject, server := liveEngine(t)
	ctx := context.Background()
	page := livePage(t, subject, server.BaseURL+"/spa")

	result, err := subject.Evaluate(ctx, page, "document.body.innerText")
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	var text string
	_ = json.Unmarshal(result.Value, &text)
	if strings.TrimSpace(text) == "" {
		t.Fatal("/spa body is empty: the engine is reading the pre-hydration shell, not the rendered DOM")
	}
}

// TestLiveNestedFrames records the one capability from issue #355's table that
// Safari's BiDi genuinely delivers: frames are separate browsing contexts, so
// a nested frame is addressable rather than a null contentDocument.
func TestLiveNestedFrames(t *testing.T) {
	subject, server := liveEngine(t)
	ctx := context.Background()
	page := livePage(t, subject, server.BaseURL+"/iframe")

	frames, err := subject.FrameTree(ctx, page)
	if err != nil {
		t.Fatalf("frame tree: %v", err)
	}
	if len(frames) != 1 || len(frames[0].Children) == 0 {
		t.Fatalf("frame tree has no child frame: %+v", frames)
	}
	child := frames[0].Children[0]
	if len(child.Children) == 0 {
		t.Fatalf("grandchild frame is not addressable: %+v", child)
	}
	if err := subject.SetActiveFrame(ctx, page, child.ID); err != nil {
		t.Fatalf("select child frame: %v", err)
	}
}

// TestLiveOverlayIsNotClickable is the acceptance test for the finding that
// reshaped issue #355.
//
// The issue expected input.performActions to click through real hit-testing
// and report interception on /overlay. Safari 27.0 implements no input module
// at all, so there is no trusted click to make. The engine must therefore
// refuse interaction outright rather than fall back to a JavaScript click(),
// which would fire the covered button's handler and report success for an
// action a human cannot perform — the exact truth defect docs/engines.md
// records against safari-attach.
func TestLiveOverlayIsNotClickable(t *testing.T) {
	subject, server := liveEngine(t)
	page := livePage(t, subject, server.BaseURL+"/overlay")
	_ = page

	if _, ok := any(subject).(engine.InteractionEngine); ok {
		t.Fatal("safari-bidi must not implement InteractionEngine while Safari ships no BiDi input module: a JS click() would bypass hit-testing and report success on /overlay")
	}
	for _, name := range subject.Capabilities().Unsupported {
		if name == "InteractionEngine" {
			return
		}
	}
	t.Fatal("InteractionEngine must be reported as unsupported")
}

// TestLivePolicyBlocksNavigation checks that the URL policies actually gate a
// live session, and records how far they reach.
func TestLivePolicyBlocksNavigation(t *testing.T) {
	subject, server := liveEngine(t)
	ctx := context.Background()
	allowlist, err := policy.ParseAllowlist([]string{"allowed.example"})
	if err != nil {
		t.Fatalf("allowlist: %v", err)
	}
	subject.Allowlist = allowlist

	page, err := subject.NewPage(ctx, engine.Context{}, "")
	if err != nil {
		t.Fatalf("new page: %v", err)
	}
	if _, err := subject.Navigate(ctx, page, server.BaseURL+"/static"); err == nil {
		t.Fatal("navigation outside the allowlist must be blocked")
	}
	if blocked := subject.BlockedRequests(); len(blocked) != 1 {
		t.Fatalf("BlockedRequests = %+v, want one denial", blocked)
	}
}

// TestLiveScreenshotRefused pins the absent capture command to a typed refusal
// rather than an empty image.
func TestLiveScreenshotRefused(t *testing.T) {
	subject, server := liveEngine(t)
	page := livePage(t, subject, server.BaseURL+"/static")
	_, err := subject.Screenshot(context.Background(), page)
	var unsupported *engine.UnsupportedOperationError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Screenshot error = %v, want a typed unsupported-operation error", err)
	}
}
