package main

import (
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-corekit/domkit"
)

// TestWithEngineHintMergesHintFields verifies the envelope-data shape of
// read --engine-hint: the document fields stay intact and js_required is a
// sibling (never inside the contract-fixed frontmatter).
func TestWithEngineHintMergesHintFields(t *testing.T) {
	document := domkit.Document{
		URL:        "https://example.com/spa",
		Title:      "SPA",
		Markdown:   "# SPA\n",
		SchemaType: "browse",
		FetchedAt:  "2026-08-03T00:00:00Z",
	}
	data := withEngineHint(document, true, "page content differs when JavaScript is disabled")
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["js_required"] != true {
		t.Errorf("js_required = %v, want true", payload["js_required"])
	}
	if payload["js_required_reason"] == "" {
		t.Error("js_required_reason missing")
	}
	if payload["url"] != "https://example.com/spa" || payload["markdown"] != "# SPA\n" {
		t.Errorf("document fields must survive the merge: %v", payload)
	}
	// The fetch-schema fields must remain exactly as rendered.
	if payload["schema_type"] != "browse" {
		t.Errorf("schema_type = %v, want browse", payload["schema_type"])
	}
}
