package state

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func rewriteStateSchema(t *testing.T, dir, name string, schema int) {
	t.Helper()
	path := filepath.Join(dir, name+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data := raw[len(fileMagic):]
	newlineIdx := bytes.IndexByte(data, '\n')
	if newlineIdx == -1 {
		t.Fatal("missing state header delimiter")
	}
	var header stateHeader
	if err := json.Unmarshal(data[:newlineIdx], &header); err != nil {
		t.Fatal(err)
	}
	header.SchemaVersion = schema
	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	updated := append([]byte{}, fileMagic...)
	updated = append(updated, headerBytes...)
	updated = append(updated, '\n')
	updated = append(updated, data[newlineIdx+1:]...)
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatal(err)
	}
}
