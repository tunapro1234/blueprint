package book

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveRoundTripPreservesRegistrationAndUnknownFields(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(t.TempDir(), "book.json")
	data := []byte(`{"orchestrator":"main","custom":"keep","agents":[{"name":"main"},{"name":"work","parent":"main","folder":"/work","status":"closed","colorOverride":"160","launch":{"codex":true,"resumeId":"11111111-1111-1111-1111-111111111111"},"future":{"keep":true}}]}`)
	os.WriteFile(path, data, 0600)
	var original map[string]any
	json.Unmarshal(data, &original)
	if err := SetArchived([]string{path}, "work", true, nil); err != nil {
		t.Fatal(err)
	}
	fleet, err := LoadFleet([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fleet.Agents["work"]; ok {
		t.Fatal("archived agent still active")
	}
	rows, err := Archives([]string{path})
	if err != nil || len(rows) != 1 || rows[0].Agent.Launch.ResumeID == "" || rows[0].Agent.ColorOverride != "160" {
		t.Fatal(rows, err)
	}
	if err := SetStatus([]string{path}, "work", "opening", "/elsewhere", Registration{}); err == nil {
		t.Fatal("launch silently restored archived record")
	}
	if err := SetArchived([]string{path}, "work", false, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	var restored map[string]any
	json.Unmarshal(after, &restored)
	old, _ := json.Marshal(original)
	new, _ := json.Marshal(restored)
	if !bytes.Equal(old, new) {
		t.Fatalf("record changed: %s", after)
	}
}

func TestArchiveRefusalsNeverModifyBooks(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	for _, tc := range []struct {
		name, rows string
		archive    bool
	}{
		{"main", `{"name":"work"}`, true},
		{"work", `{"name":"work"},{"name":"child","parent":"work"}`, true},
		{"work", `{"name":"work","archivedAt":"old","parent":"gone"}`, false},
		{"work", `{"name":"work"},{"name":"work"}`, true},
	} {
		t.Run(tc.rows, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "book.json")
			before := []byte(`{"orchestrator":"main","agents":[{"name":"main"},` + tc.rows + `]}`)
			os.WriteFile(path, before, 0600)
			if err := SetArchived([]string{path}, tc.name, tc.archive, nil); err == nil {
				t.Fatal("unsafe change accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("refused operation modified book")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "book.json")
	before := []byte(`{"orchestrator":"main","agents":[{"name":"work"}]}`)
	os.WriteFile(path, before, 0600)
	if err := SetArchived([]string{path}, "work", true, func(Agent) error { return errors.New("live native process") }); err == nil {
		t.Fatal("ignored runtime refusal")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("runtime refusal modified book")
	}
}
