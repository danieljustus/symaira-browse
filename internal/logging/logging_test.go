package logging

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
)

func TestInitDebugWritesStructuredLogToStderr(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStderr := os.Stderr
	os.Stderr = writer
	t.Setenv("SYMBROWSE_LOG_LEVEL", "debug")
	t.Setenv("SYMBROWSE_LOG_FORMAT", "json")
	t.Cleanup(func() {
		os.Stderr = originalStderr
		_ = reader.Close()
		_ = writer.Close()
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	})

	Init()
	slog.Default().Debug("configuration loaded", "source", "test")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("stderr is not one structured JSON record: %q", data)
	}
}
