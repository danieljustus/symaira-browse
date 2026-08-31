package output

import (
	"bytes"
	"strings"
	"testing"
)

func writeHumanForTest(t *testing.T, data any) string {
	t.Helper()
	var buffer bytes.Buffer
	if err := Write(&buffer, OK(data, nil), FormatText); err != nil {
		t.Fatal(err)
	}
	return buffer.String()
}

func TestWriteHumanNavigationPayload(t *testing.T) {
	got := writeHumanForTest(t, map[string]any{
		"action":      "open",
		"url":         "https://example.com/",
		"http_status": float64(200),
	})
	want := "open https://example.com/ (HTTP 200)\n"
	if got != want {
		t.Fatalf("human navigation output = %q, want %q", got, want)
	}
}

func TestWriteHumanSnapshotPayload(t *testing.T) {
	got := writeHumanForTest(t, map[string]any{
		"snapshot_id": "snap-1",
		"tree":        "- document \"Example Domain\" [ref=e1]\n  - heading \"Example Domain\" [ref=e2]",
		"refs":        map[string]any{"e1": map[string]any{"role": "document"}},
	})
	want := "snap-1 tree:\n- document \"Example Domain\" [ref=e1]\n  - heading \"Example Domain\" [ref=e2]\n"
	if got != want {
		t.Fatalf("human snapshot output = %q, want %q", got, want)
	}
	if strings.Contains(got, "map[") {
		t.Fatalf("snapshot output contains Go map syntax: %q", got)
	}
}

func TestWriteHumanStatePayload(t *testing.T) {
	got := writeHumanForTest(t, map[string]any{
		"saved": "demo",
		"metadata": map[string]any{
			"name":           "demo",
			"schema_version": float64(3),
			"key_source":     "environment",
			"origins": []any{map[string]any{
				"origin":               "https://example.com",
				"cookie_count":         float64(1),
				"local_storage_keys":   float64(2),
				"session_storage_keys": float64(0),
			}},
		},
	})
	for _, want := range []string{"saved: demo\n", "schema version: 3\n", "key source: environment\n", "https://example.com (cookies=1, local=2, session=0)\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("human state output = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "map[") {
		t.Fatalf("state output contains Go map syntax: %q", got)
	}
}

func TestWriteHumanCookiesPayloadDoesNotExposeValues(t *testing.T) {
	got := writeHumanForTest(t, map[string]any{
		"origin": "https://example.com",
		"cookies": []any{map[string]any{
			"name":      "session",
			"value":     "s3cret",
			"domain":    ".example.com",
			"path":      "/",
			"secure":    true,
			"http_only": true,
		}},
	})
	if !strings.Contains(got, "origin: https://example.com\ncookies:\n- session domain=.example.com path=/ secure httpOnly\n") {
		t.Fatalf("human cookie output = %q", got)
	}
	if strings.Contains(got, "s3cret") || strings.Contains(got, "map[") {
		t.Fatalf("cookie output leaked or dumped a map: %q", got)
	}
}

func TestWriteHumanTabsPayload(t *testing.T) {
	got := writeHumanForTest(t, map[string]any{
		"active": "t1",
		"tabs": []any{
			map[string]any{"id": "t1", "label": "research", "url": "https://example.com", "active": true},
			map[string]any{"id": "t2", "url": "about:blank", "active": false},
		},
	})
	want := "active: t1\ntabs:\n- t1 \"research\" https://example.com [active]\n- t2 about:blank\n"
	if got != want {
		t.Fatalf("human tabs output = %q, want %q", got, want)
	}
}

func TestWriteHumanUnknownPayloadUsesIndentedJSON(t *testing.T) {
	got := writeHumanForTest(t, map[string]any{"z": "last", "a": float64(1)})
	want := "{\n  \"a\": 1,\n  \"z\": \"last\"\n}\n"
	if got != want {
		t.Fatalf("generic human output = %q, want %q", got, want)
	}
	if strings.Contains(got, "map[") {
		t.Fatalf("generic output contains Go map syntax: %q", got)
	}
}
