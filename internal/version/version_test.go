package version

import "testing"

func TestInfoUsesVersionkitContract(t *testing.T) {
	info := Info("v0.1.0")
	if info.Tool != "symbrowse" || info.Version != "v0.1.0" || info.SchemaVersion != SchemaVersion {
		t.Fatalf("info = %#v", info)
	}
}
