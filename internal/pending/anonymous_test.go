package pending

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyAnonymousSpoolCannotFlushAndIsPreserved(t *testing.T) {
	// "bilinmiyor" is the legacy unknown label written by older bp versions.
	for _, sender := range []string{"", "unknown", "bilinmiyor"} {
		dir := t.TempDir()
		entry := Entry{TS: 1788885626, From: sender, Kind: "msg", Text: "legacy anonymous message"}
		if err := Append(dir, "target", entry); err == nil {
			t.Fatal("new anonymous message was accepted")
		}
		if err := os.MkdirAll(filepath.Join(dir, "pending"), 0755); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		file := path(dir, "target")
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		snapshot, err := Load(dir, "target")
		if err != nil || len(snapshot.Entries) != 0 || len(snapshot.Held) != 1 || snapshot.Held[0].Reason != ReasonAnonymousSender {
			t.Fatal(snapshot, err)
		}
		after, err := os.ReadFile(file)
		if err != nil || string(after) != string(data) {
			t.Fatal("legacy spool evidence changed", err)
		}
	}
}
