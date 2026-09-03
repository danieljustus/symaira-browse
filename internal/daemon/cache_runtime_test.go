package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-browse/internal/budget"
)

func TestCacheRuntimeRetrievesFullContentAndRange(t *testing.T) {
	dir := t.TempDir()
	cache := budget.NewCache(dir, time.Hour)
	id, err := cache.Store([]byte("line one\nline two\nline three"))
	if err != nil {
		t.Fatal(err)
	}

	runtime := NewCacheRuntime(dir, time.Hour)
	data, _, err := runtime.Handle(context.Background(), Frame{
		Cmd:  "cache.get",
		Args: marshalArgsForTest(map[string]any{"cache_id": id, "range": "2-3"}),
	})
	if err != nil {
		t.Fatalf("cache.get: %v", err)
	}
	response, ok := data.(map[string]any)
	if !ok {
		t.Fatalf("response = %T, want map", data)
	}
	if response["cache_id"] != id || response["range"] != "2-3" || response["content"] != "line two\nline three" {
		t.Fatalf("response = %#v", response)
	}

	full, _, err := runtime.Handle(context.Background(), Frame{
		Cmd:  "cache.get",
		Args: marshalArgsForTest(map[string]any{"cache_id": id}),
	})
	if err != nil {
		t.Fatalf("cache.get full: %v", err)
	}
	fullResponse := full.(map[string]any)
	if !strings.Contains(fullResponse["content"].(string), "line one") {
		t.Fatalf("full response = %#v", fullResponse)
	}
}

func TestCacheRuntimeRejectsInvalidID(t *testing.T) {
	runtime := NewCacheRuntime(t.TempDir(), time.Hour)
	_, _, err := runtime.Handle(context.Background(), Frame{
		Cmd:  "cache.get",
		Args: marshalArgsForTest(map[string]any{"cache_id": "../../secret"}),
	})
	if err == nil || !strings.Contains(err.Error(), "cache entry not found") {
		t.Fatalf("invalid cache id error = %v", err)
	}
}
