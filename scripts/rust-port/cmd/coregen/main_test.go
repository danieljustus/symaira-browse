package main

import (
	"encoding/json"
	"testing"
)

func TestEqualExceptCacheModes(t *testing.T) {
	t.Parallel()

	base := fixture{
		SchemaVersion: 1,
		Oracle:        oracle{Commit: "abc", Release: "v0.8.0"},
		Cache: cacheCase{
			ID:          "out_060504030201",
			Content:     "full content",
			Metadata:    "{}",
			ContentMode: 0o600,
			MetaMode:    0o644,
		},
	}
	encode := func(f fixture) []byte {
		b, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return append(b, '\n')
	}

	same := base
	if !equalExceptCacheModes(encode(base), encode(same)) {
		t.Error("identical fixtures must compare equal")
	}

	modesDiffer := base
	modesDiffer.Cache.ContentMode = 0o666
	modesDiffer.Cache.MetaMode = 0o666
	if !equalExceptCacheModes(encode(base), encode(modesDiffer)) {
		t.Error("mode-only difference must be relaxed")
	}

	contentDiffers := base
	contentDiffers.Cache.Content = "tampered"
	if equalExceptCacheModes(encode(base), encode(contentDiffers)) {
		t.Error("content difference must NOT be relaxed")
	}

	if equalExceptCacheModes([]byte("{"), encode(base)) {
		t.Error("invalid JSON must not compare equal")
	}
}
