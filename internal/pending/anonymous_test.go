package pending

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyAnonymousSpoolCannotFlushAndIsPreserved(t *testing.T) {
	for _, sender := range []string{"", "bilinmiyor"} {
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
		entries, _, err := Load(dir, "target")
		if err == nil || !strings.Contains(err.Error(), "anonymous delivery blocked") || len(entries) != 0 {
			t.Fatal(entries, err)
		}
		after, err := os.ReadFile(file)
		if err != nil || string(after) != string(data) {
			t.Fatal("legacy spool evidence changed", err)
		}
	}
}
