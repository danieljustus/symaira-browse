package flows

import (
	"testing"
)

// FuzzFlowParser feeds arbitrary bytes through the flow parser (issue #67):
// parsing must never panic and must be deterministic for identical input.
func FuzzFlowParser(f *testing.F) {
	f.Add([]byte("version: 1\nname: demo\nsteps:\n  - open:\n      url: https://example.com\n"), "demo.yaml")
	f.Add([]byte("garbage"), "x.yaml")
	f.Add([]byte(""), "")
	f.Add([]byte("version: 1\nsteps:\n  - fill:\n      selector: '@e1'\n      value: ''\n"), "t.yaml")
	f.Fuzz(func(t *testing.T, data []byte, source string) {
		first, err := Parse(data, source)
		if err != nil {
			return // invalid input must error, not panic
		}
		second, err := Parse(data, source)
		if err != nil {
			t.Fatalf("second parse failed after first succeeded: %v", err)
		}
		if len(first.Steps) != len(second.Steps) {
			t.Fatalf("parse not deterministic: %d vs %d steps", len(first.Steps), len(second.Steps))
		}
	})
}
