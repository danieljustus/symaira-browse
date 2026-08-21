package archive

import (
	"testing"
)

func TestRewriteURL_WithTimestamp(t *testing.T) {
	got := RewriteURL("https://example.com", "20260101120000")
	want := "https://web.archive.org/web/20260101120000/https://example.com"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRewriteURL_LatestSnapshot(t *testing.T) {
	got := RewriteURL("https://example.com", "")
	want := "https://web.archive.org/web/*/https://example.com"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRewriteURL_WithQueryAndPath(t *testing.T) {
	got := RewriteURL("https://example.com/path?q=1", "20260101120000")
	want := "https://web.archive.org/web/20260101120000/https://example.com/path?q=1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseWaybackURL_Valid(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantURL  string
		wantBool bool
	}{
		{
			name:     "standard archive URL",
			input:    "https://web.archive.org/web/20260101120000/https://example.com",
			wantURL:  "https://example.com",
			wantBool: true,
		},
		{
			name:     "with id_ modifier",
			input:    "https://web.archive.org/web/20260101120000id_/https://example.com",
			wantURL:  "https://example.com",
			wantBool: true,
		},
		{
			name:     "with if_ modifier",
			input:    "https://web.archive.org/web/20260101120000if_/https://example.com",
			wantURL:  "https://example.com",
			wantBool: true,
		},
		{
			name:     "wildcard timestamp",
			input:    "https://web.archive.org/web/*/https://example.com",
			wantURL:  "https://example.com",
			wantBool: true,
		},
		{
			name:     "with path and query",
			input:    "https://web.archive.org/web/20260101120000/https://example.com/path?q=1",
			wantURL:  "https://example.com/path?q=1",
			wantBool: true,
		},
		{
			name:     "non-wayback URL",
			input:    "https://example.com",
			wantURL:  "",
			wantBool: false,
		},
		{
			name:     "incomplete wayback URL",
			input:    "https://web.archive.org/web/",
			wantURL:  "",
			wantBool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotBool := ParseWaybackURL(tt.input)
			if gotBool != tt.wantBool {
				t.Errorf("ok = %v, want %v", gotBool, tt.wantBool)
			}
			if gotURL != tt.wantURL {
				t.Errorf("got %q, want %q", gotURL, tt.wantURL)
			}
		})
	}
}

func TestStripWaybackToolbar_RemovesToolbar(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head><title>Test</title></head>
<body>
<div id="wm-ipp-base" style="display:block"><p>Wayback toolbar</p></div>
<div id="wm-ipp" style="display:none"><p>More toolbar</p></div>
<h1>Main Content</h1>
<p>This is the actual content.</p>
</body>
</html>`

	got := StripWaybackToolbar(html)

	if got == html {
		t.Error("toolbar was not stripped")
	}
	if contains(got, "wm-ipp-base") {
		t.Error("wm-ipp-base was not removed")
	}
	if contains(got, "wm-ipp") {
		t.Error("wm-ipp was not removed")
	}
	if !contains(got, "Main Content") {
		t.Error("main content was accidentally removed")
	}
	if !contains(got, "actual content") {
		t.Error("actual content was accidentally removed")
	}
}

func TestStripWaybackToolbar_RemovesArchiveScripts(t *testing.T) {
	html := `<html><head>
<script src="https://archive.org/wayback-toolbar.js"></script>
<script>var wombat = "something";</script>
<script>normal script content</script>
</head><body><p>Content</p></body></html>`

	got := StripWaybackToolbar(html)

	if contains(got, "archive.org") {
		t.Error("archive.org script was not removed")
	}
	if !contains(got, "normal script") {
		t.Error("normal script was accidentally removed")
	}
}

func TestStripWaybackToolbar_NoToolbar(t *testing.T) {
	html := `<html><head><title>Test</title></head><body><p>Content</p></body></html>`
	got := StripWaybackToolbar(html)
	if got != html {
		t.Error("clean HTML should not be modified")
	}
}

func TestRemoveElementByClass(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		class    string
		wantHTML string
	}{
		{
			name:     "class not present",
			html:     "<div>no class here</div>",
			class:    "target",
			wantHTML: "<div>no class here</div>",
		},
		{
			name:     "class present, valid tag, successfully removed",
			html:     `<html><body><div class="target">to be removed</div><span>keep me</span></body></html>`,
			class:    "target",
			wantHTML: `<html><body><span>keep me</span></body></html>`,
		},
		{
			name:     "class present but no closing tag",
			html:     `<div class="target">no closing tag`,
			class:    "target",
			wantHTML: `<div class="target">no closing tag`,
		},
		{
			name:     "class present, closing tag found but no trailing >",
			html:     `<div class="target">no trailing bracket</div`,
			class:    "target",
			wantHTML: `</div`,
		},
		{
			name:     "class present at index 0",
			html:     `<div class="target">start</div>`,
			class:    "target",
			wantHTML: ``,
		},
		{
			name:     "class present with trailing newline",
			html:     "<div class=\"target\">with newline</div>\n<p>next</p>",
			class:    "target",
			wantHTML: "<p>next</p>",
		},
		{
			name:     "case insensitive class name",
			html:     `<div class="TARGET">case test</div>`,
			class:    "target",
			wantHTML: ``,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := removeElementByClass(tt.html, tt.class)
			if got != tt.wantHTML {
				t.Errorf("got %q, want %q", got, tt.wantHTML)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(substr) > 0 && (len(s) >= len(substr)) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
