package msgq

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readLog(t *testing.T, root string) []LogEntry {
	t.Helper()
	f, err := os.Open(MessageLogPath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []LogEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e LogEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestFinishAppendsMessageLogAndReindexFillsGaps(t *testing.T) {
	root := t.TempDir()
	q := New(root)
	id, err := q.Enqueue("worker", "lead", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Cancel(id); err != nil {
		t.Fatal(err)
	}
	log := readLog(t, root)
	if len(log) != 1 || log[0].ID != id || log[0].Status != "canceled (by operator)" || log[0].Msg != "hello" {
		t.Fatalf("log = %+v", log)
	}
	// A done record written by an older binary is missing from the log.
	old := Message{ID: "q1", To: "a", From: "b", Msg: "older", TS: 1, Finished: 2, Status: "delivered"}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(root, "done", "q1.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := q.Reindex()
	if err != nil || n != 1 {
		t.Fatalf("reindex = %d %v", n, err)
	}
	if n, _ := q.Reindex(); n != 0 {
		t.Fatalf("second reindex added %d", n)
	}
	if log := readLog(t, root); len(log) != 2 || log[1].ID != "q1" {
		t.Fatalf("log = %+v", log)
	}
}
