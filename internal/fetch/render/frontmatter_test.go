package render

import (
	"testing"
)

func TestExtractWaybackTimestamp(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   string
	}{
		{
			name:   "non-wayback URL",
			rawURL: "https://example.com/page",
			want:   "",
		},
		{
			name:   "wayback wildcard URL",
			rawURL: "https://web.archive.org/web/*/https://example.com/page",
			want:   "",
		},
		{
			name:   "wayback URL with valid timestamp",
			rawURL: "https://web.archive.org/web/20260101120000/https://example.com/page",
			want:   "20260101120000",
		},
		{
			name:   "wayback URL with timestamp and modifier",
			rawURL: "https://web.archive.org/web/20260101120000id_/https://example.com/page",
			want:   "20260101120000id_",
		},
		{
			name:   "wayback URL with empty remainder",
			rawURL: "https://web.archive.org/web/",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractWaybackTimestamp(tt.rawURL)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
