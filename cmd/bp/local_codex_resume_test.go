package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexResumeFlagSelection(t *testing.T) {
	id := "01a08084-80c4-75a3-bbfc-3b7ee1645d2a"
	for _, tc := range []struct {
		args         []string
		target       string
		remote, last bool
	}{
		{[]string{"resume", "--yolo"}, "", false, false},
		{[]string{"--search", "resume", id, "--yolo"}, id, false, false},
		{[]string{"-c", "x=\"resume\"", "resume", "--last", "--yolo"}, "", false, true},
		{[]string{"resume", id, "--remote", "unix://"}, id, true, false},
		{[]string{"--remote=unix://", "resume", id}, id, true, false},
		{[]string{"resume", "--model", "something", "named session"}, "named session", false, false},
	} {
		r := parseCodexResume(tc.args)
		if r.command < 0 || r.target != tc.target || r.remote != tc.remote || r.last != tc.last {
			t.Fatalf("%v: %+v", tc.args, r)
		}
	}
	if r := parseCodexResume([]string{"exec", "resume", "--last"}); r.command >= 0 {
		t.Fatal("batch resume intercepted")
	}
}

func TestCodexResumeSelectionPreservesChoice(t *testing.T) {
	rows := []codexResumeSession{{ID: "one", Title: "recent", Modified: time.Now()}, {ID: "two", Title: "older"}}
	for _, tc := range []struct {
		last                bool
		target, input, want string
		err                 bool
	}{
		{false, "", "2\n", "two", false}, {true, "", "", "one", false},
		{false, "older", "", "two", false}, {false, "", "\n", "", false},
		{false, "", "99\n", "", true},
	} {
		var out bytes.Buffer
		id, e := chooseCodexResume(rows, tc.last, tc.target, strings.NewReader(tc.input), &out)
		if id != tc.want || (e != nil) != tc.err {
			t.Fatalf("%+v: %q %v", tc, id, e)
		}
	}
	rows[1].Title = "recent"
	if _, e := chooseCodexResume(rows, false, "recent", strings.NewReader(""), &bytes.Buffer{}); e == nil {
		t.Fatal("ambiguous title chosen")
	}
}

func TestCodexResumeMetadataUsesPhysicalCWDAndExcludesSubagents(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	link := filepath.Join(t.TempDir(), "linked")
	if e := os.Symlink(cwd, link); e != nil {
		t.Fatal(e)
	}
	id := "01a08084-80c4-75a3-bbfc-3b7ee1645d2a"
	dir := filepath.Join(home, "sessions", "2026", "09", "08")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	for name, source := range map[string]any{id: "cli", "01a08084-80c4-75a3-bbfc-3b7ee1645d2b": map[string]string{"subagent": "child"}} {
		b, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": name, "cwd": link, "source": source}})
		if e := os.WriteFile(filepath.Join(dir, "rollout-"+name+".jsonl"), append(b, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
	rows, e := codexResumeSessions(home, cwd, false)
	if e != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("%+v %v", rows, e)
	}
}

func TestResumePickerDoesNotRenderTerminalControlFromCWD(t *testing.T) {
	var out bytes.Buffer
	rows := []codexResumeSession{{ID: "one", Title: "work", CWD: "/tmp/\x1b[2J"}}
	if _, err := chooseResumeSession("Claude", rows, false, "", strings.NewReader("1\n"), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b") {
		t.Fatal("native metadata emitted terminal controls")
	}
}
