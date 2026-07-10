package wa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSendPublishesOnlyCompleteVisibleJSON(t *testing.T) {
	outbox := t.TempDir()
	if err := Send(outbox, "agent", "target", "", "hello"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(outbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || strings.HasPrefix(entries[0].Name(), ".") || filepath.Ext(entries[0].Name()) != ".json" {
		t.Fatalf("published entries=%v", entries)
	}
	data, err := os.ReadFile(filepath.Join(outbox, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var record Outgoing
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Agent != "agent" || record.Text != "[agent] hello" || record.To == nil || *record.To != "target" {
		t.Fatalf("record=%+v", record)
	}
}
