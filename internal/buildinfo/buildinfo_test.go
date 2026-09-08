package buildinfo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonRecordRejectsReusedPIDAndChangedExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon-runtime.json")
	if err := Record(path); err != nil {
		t.Fatal(err)
	}
	current, verified := Recorded(path)
	if current == nil || current.StartTicks == "" {
		t.Skip("process identity unavailable on this OS")
	}
	if verified != "verified executable" {
		t.Fatal(verified)
	}
	for _, change := range []string{"pid-start", "sha"} {
		bad := *current
		if change == "pid-start" {
			bad.StartTicks += "0"
		} else {
			bad.SHA256 = "different"
		}
		data, _ := json.Marshal(bad)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		_, status := Recorded(path)
		if !strings.HasPrefix(status, "unverified:") {
			t.Fatalf("stale daemon identified as current: %s", status)
		}
	}
}
