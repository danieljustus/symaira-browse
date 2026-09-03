package safaribidi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/testserver"
)

// TestProbeSurface enumerates which WebDriver BiDi commands Safari actually
// implements and whether it delivers events. It is the harness behind the
// capability matrix in docs/engines.md and behind this engine's Capabilities
// set, and it is the thing to re-run when a new Safari ships: a command that
// moves from ABSENT to PRESENT is a capability this engine may then claim.
//
// It needs a real Safari and no Safari running for normal browsing:
//
//	SYMBROWSE_SAFARI_BIDI=1 go test ./internal/engine/safaribidi -run ProbeSurface -v
func TestProbeSurface(t *testing.T) {
	if os.Getenv("SYMBROWSE_SAFARI_BIDI") == "" {
		t.Skip("set SYMBROWSE_SAFARI_BIDI=1 to run the live Safari surface probe")
	}
	server := testserver.New(t)
	ctx := context.Background()
	live, err := connect(ctx, Options{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = live.Close(ctx) }()
	conn := live.conn

	var mu sync.Mutex
	var frames []string
	conn.addHandler(func(method string, params json.RawMessage) {
		mu.Lock()
		frames = append(frames, method)
		mu.Unlock()
	})

	call := func(m string, p any, out any) error {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return conn.Execute(c, m, p, out)
	}

	var tree struct {
		Contexts []struct {
			Context string `json:"context"`
		} `json:"contexts"`
	}
	_ = call("browsingContext.getTree", map[string]any{}, &tree)
	page := tree.Contexts[0].Context

	// Command surface enumeration: "unknown command" means absent, anything
	// else means present but rejecting these arguments.
	commands := []struct {
		method string
		params any
	}{
		{"session.status", map[string]any{}},
		{"session.subscribe", map[string]any{"events": []string{"browsingContext.load"}}},
		{"session.unsubscribe", map[string]any{"events": []string{"browsingContext.load"}}},
		{"browsingContext.activate", map[string]any{"context": page}},
		{"browsingContext.captureScreenshot", map[string]any{"context": page}},
		{"browsingContext.locateNodes", map[string]any{"context": page, "locator": map[string]any{"type": "css", "value": "body"}}},
		{"browsingContext.print", map[string]any{"context": page}},
		{"browsingContext.reload", map[string]any{"context": page}},
		{"browsingContext.traverseHistory", map[string]any{"context": page, "delta": 0}},
		{"browsingContext.setViewport", map[string]any{"context": page, "viewport": map[string]any{"width": 900, "height": 700}}},
		{"browsingContext.handleUserPrompt", map[string]any{"context": page}},
		{"script.evaluate", map[string]any{"expression": "1", "target": map[string]any{"context": page}, "awaitPromise": false}},
		{"script.callFunction", map[string]any{"functionDeclaration": "()=>1", "target": map[string]any{"context": page}, "awaitPromise": false}},
		{"script.getRealms", map[string]any{}},
		{"script.addPreloadScript", map[string]any{"functionDeclaration": "()=>{}"}},
		{"script.disown", map[string]any{"target": map[string]any{"context": page}, "handles": []string{}}},
		{"input.performActions", map[string]any{"context": page, "actions": []any{}}},
		{"input.releaseActions", map[string]any{"context": page}},
		{"input.setFiles", map[string]any{"context": page}},
		{"network.addIntercept", map[string]any{"phases": []string{"beforeRequestSent"}}},
		{"network.continueRequest", map[string]any{"request": "x"}},
		{"network.setCacheBehavior", map[string]any{"cacheBehavior": "default"}},
		{"storage.getCookies", map[string]any{}},
		{"storage.setCookie", map[string]any{}},
		{"storage.deleteCookies", map[string]any{}},
		{"emulation.setGeolocationOverride", map[string]any{}},
		{"permissions.setPermission", map[string]any{}},
		{"webExtension.install", map[string]any{}},
		{"bluetooth.simulateAdapter", map[string]any{}},
	}
	for _, c := range commands {
		err := call(c.method, c.params, nil)
		var commandErr *CommandError
		switch {
		case err == nil:
			t.Logf("SURFACE %-38s PRESENT (accepted)", c.method)
		case !errors.As(err, &commandErr):
			t.Logf("SURFACE %-38s ERROR    %v", c.method, err)
		case commandErr.Code == "unknown command":
			// Safari answers "unknown command" both for a missing command and
			// for a missing whole module ("'input' domain was not found").
			t.Logf("SURFACE %-38s ABSENT   %s", c.method, commandErr.Message)
		default:
			// Present, but rejecting these particular arguments.
			t.Logf("SURFACE %-38s PRESENT  (%s: %s)", c.method, commandErr.Code, commandErr.Message)
		}
	}

	// Event delivery, tested hard: subscribe globally, subscribe per context,
	// navigate, and give Safari a full second to emit anything at all.
	_ = call("session.subscribe", map[string]any{"events": []string{
		"browsingContext.load", "browsingContext.domContentLoaded", "browsingContext.contextCreated",
		"log.entryAdded", "network.beforeRequestSent", "network.responseCompleted", "script.message",
	}}, nil)
	_ = call("session.subscribe", map[string]any{
		"events": []string{"browsingContext.load", "log.entryAdded"}, "contexts": []string{page},
	}, nil)
	mu.Lock()
	frames = nil
	mu.Unlock()
	_ = call("browsingContext.navigate", map[string]any{"context": page, "url": server.BaseURL + "/static", "wait": "complete"}, nil)
	_ = call("script.evaluate", map[string]any{"expression": "console.log('probe'); console.error('probe-err')", "target": map[string]any{"context": page}, "awaitPromise": false}, nil)
	time.Sleep(2 * time.Second)
	mu.Lock()
	t.Logf("EVENTS delivered: %v", frames)
	mu.Unlock()

	// Cross-origin iframe reachability.
	_ = call("browsingContext.navigate", map[string]any{"context": page, "url": server.BaseURL + "/iframe", "wait": "complete"}, nil)
	var frameTree struct {
		Contexts []struct {
			Context  string `json:"context"`
			URL      string `json:"url"`
			Children []struct {
				Context  string `json:"context"`
				URL      string `json:"url"`
				Children []struct {
					Context string `json:"context"`
					URL     string `json:"url"`
				} `json:"children"`
			} `json:"children"`
		} `json:"contexts"`
	}
	_ = call("browsingContext.getTree", map[string]any{}, &frameTree)
	blob, _ := json.Marshal(frameTree)
	t.Logf("FRAME TREE %s", blob)
}
