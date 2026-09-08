package book

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeTitleIncrementalAndSessionIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	appendText := func(s string) {
		f, e := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		if _, e = f.WriteString(s); e != nil {
			t.Fatal(e)
		}
	}
	appendText("{\"type\":\"custom-title\",\"customTitle\":\"old\"}\n{\"type\":\"custom-title\",\"customTitle\":\"orch\"}\n")
	value, err := ReadNativeTitle(path, "thread", nil)
	if err != nil || value.Text != "orch" {
		t.Fatal(value, err)
	}
	appendText(strings.Repeat("{\"type\":\"progress\"}\n", 100000))
	next, err := ReadNativeTitle(path, "thread", &value)
	if err != nil || next.Text != "orch" || next.Offset <= value.Offset {
		t.Fatal(next, err)
	}
	appendText(`{"type":"custom-title","customTitle":"partial"}`)
	partial, err := ReadNativeTitle(path, "thread", &next)
	if err != nil || partial != next {
		t.Fatal(partial, err)
	}
	appendText("\n")
	done, err := ReadNativeTitle(path, "thread", &partial)
	if err != nil || done.Text != "partial" {
		t.Fatal(done, err)
	}
	appendText("{\"type\":\"custom-title\",\"customTitle\":\"fake\",\"isSidechain\":true}\n{\"type\":\"custom-title\",\"customTitle\":\"foreign\",\"sessionId\":\"other\"}\n")
	safe, err := ReadNativeTitle(path, "thread", &done)
	if err != nil || safe.Text != "partial" {
		t.Fatal(safe, err)
	}
	other := filepath.Join(t.TempDir(), "other.jsonl")
	os.WriteFile(other, []byte("{}\n"), 0600)
	reset, err := ReadNativeTitle(other, "another", &safe)
	if err != nil || reset.Text != "" {
		t.Fatal(reset, err)
	}
}
