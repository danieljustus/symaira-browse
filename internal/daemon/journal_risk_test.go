package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/danieljustus/symaira-browse/internal/journal"
)

func TestJournalUsesUnknownRiskClassForUnclassifiedCommand(t *testing.T) {
	j, err := journal.New(journal.Options{Dir: t.TempDir(), Session: "default"})
	if err != nil {
		t.Fatal(err)
	}
	runtime := newJournalTestRuntime(t, j)
	args, _ := json.Marshal(map[string]any{})

	_, _, err = runtime.HandleWithDecider(context.Background(), Frame{
		Cmd:     "definitely.not.a.command",
		Session: "default",
		Args:    args,
	}, "policy")
	if err == nil {
		t.Fatal("expected unknown command to fail")
	}

	entries, err := j.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if got := entries[0].RiskClass; got != "unknown" {
		t.Fatalf("risk class = %q, want unknown", got)
	}
}
