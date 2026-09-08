package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClaudeResumeResolution(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(root, "projects")
	dir := filepath.Join(projects, strings.ReplaceAll(real, "/", "-"))
	// t.TempDir can include punctuation that Claude encodes too.
	b := []byte(real)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	dir = filepath.Join(projects, string(b))
	_ = os.MkdirAll(dir, 0700)
	id := "2832a3a6-1234-1234-1234-123456789012"
	path := filepath.Join(dir, id+".jsonl")
	_ = os.WriteFile(path, []byte("{}\n"), 0600)
	for _, args := range [][]string{{"--dangerously-skip-permissions", "-c"}, {"--continue"}, {"--resume", id}, {"--resume=" + id}, {"-r", id}} {
		got, rewritten, err := claudeResumeArgs(args, alias, projects)
		if err != nil || got != id || len(rewritten) < 2 || rewritten[0] != "--resume" || rewritten[1] != id {
			t.Fatalf("%v -> %s %v %v", args, got, rewritten, err)
		}
	}
	if got := physicalPath(alias); got != real {
		t.Fatal(got)
	}
	args := []string{"--append-system-prompt", "-c"}
	got, unchanged, err := claudeResumeArgs(args, alias, projects)
	if err != nil || got != "" || !reflect.DeepEqual(args, unchanged) {
		t.Fatalf("prompt parsed as flag: %s %v %v", got, unchanged, err)
	}
	for _, args := range [][]string{{"--resume"}, {"-r"}, {"--resume="}, {"--resume", "some-title"}, {"--dangerously-skip-permissions", "--resume", "--model", "selected"}} {
		id, got, err := claudeResumeArgs(args, alias, projects)
		if err != nil || id != "" || !reflect.DeepEqual(got, args) {
			t.Fatal("native resume arguments changed", args, id, got, err)
		}
	}
	fork := []string{"-c", "--fork-session"}
	got, unchanged, err = claudeResumeArgs(fork, alias, projects)
	if err != nil || got != "" || !reflect.DeepEqual(fork, unchanged) {
		t.Fatal("intentional fork changed")
	}
	second := filepath.Join(dir, "3832a3a6-1234-1234-1234-123456789012.jsonl")
	_ = os.WriteFile(second, []byte("{}\n"), 0600)
	stamp := time.Now().Add(-time.Minute)
	_ = os.Chtimes(path, stamp, stamp)
	_ = os.Chtimes(second, stamp, stamp)
	if _, _, err := claudeResumeArgs([]string{"-c"}, alias, projects); err == nil {
		t.Fatal("ambiguous latest selected silently")
	}
	later := stamp.Add(time.Second)
	_ = os.Chtimes(second, later, later)
	got, _, err = claudeResumeArgs([]string{"-c"}, alias, projects)
	if err != nil || !strings.HasPrefix(got, "3832") {
		t.Fatal(got, err)
	}
}
