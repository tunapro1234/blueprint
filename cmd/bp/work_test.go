package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/identity"
)

func runWork(t *testing.T, a *app, args ...string) (string, error) {
	t.Helper()
	out := testOutput(t)
	a.out = out
	err := a.run(append([]string{"work"}, args...))
	return readTestOutput(t, out), err
}

func as(a *app, who string) {
	a.resolveSender = func() identity.Identity { return identity.Identity{Label: who, Certain: true, Source: "tmux"} }
}

func TestWorkCLIEndToEnd(t *testing.T) {
	a := newRoomTestApp(t, "lead")
	root, _ := filepath.EvalSymlinks(t.TempDir())
	t.Chdir(root)
	if err := os.WriteFile("result.txt", []byte("42\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := runWork(t, a, "project", "create", "demo", "--goal", "answer the question", "--root", "."); err != nil {
		t.Fatal(err)
	}
	if out, err := runWork(t, a, "task", "add", "demo", "compute", "the", "answer", "--spec", "write result.txt"); err != nil || !strings.Contains(out, "t1 [pending] compute the answer") {
		t.Fatalf("task add: %v\n%s", err, out)
	}

	as(a, "ada")
	out, err := runWork(t, a, "task", "claim", "demo", "t1", "--lease", "10m", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var claimed struct {
		Fence int64  `json:"fence"`
		Owner string `json:"owner"`
	}
	if err := json.Unmarshal([]byte(out), &claimed); err != nil || claimed.Fence != 1 || claimed.Owner != "ada" {
		t.Fatalf("claim json: %v\n%s", err, out)
	}
	if _, err := runWork(t, a, "task", "checkpoint", "demo", "t1", "half", "way"); err == nil || !strings.Contains(err.Error(), "--fence is required") {
		t.Fatalf("checkpoint without fence: %v", err)
	}
	if _, err := runWork(t, a, "task", "checkpoint", "demo", "t1", "--fence", "1", "--progress", "50", "half", "way"); err != nil {
		t.Fatal(err)
	}
	if out, err := runWork(t, a, "task", "submit", "demo", "t1", "--fence", "1", "--artifact", "result.txt", "--revision", "r1"); err != nil || !strings.Contains(out, "submission 1: sha256:") {
		t.Fatalf("submit: %v\n%s", err, out)
	}

	// The submitter cannot verify its own work.
	if _, err := runWork(t, a, "task", "verify", "demo", "t1", "--pass", "--checks", "trust me"); err == nil {
		t.Fatal("self-verification accepted")
	}
	as(a, "bob")
	// A validator that exits 2 is not a result: nothing is recorded.
	if _, err := runWork(t, a, "task", "verify", "demo", "t1", "--run", "--", "/bin/sh", "-c", "exit 2"); err == nil || !strings.Contains(err.Error(), "nothing recorded") {
		t.Fatalf("exit 2: %v", err)
	}
	out, err = runWork(t, a, "task", "verify", "demo", "t1", "--run", "--", "/bin/sh", "-c", "grep -qx 42 result.txt")
	if err != nil || !strings.Contains(out, "verified") || !strings.Contains(out, "by bob: pass") {
		t.Fatalf("verify --run: %v\n%s", err, out)
	}
	show, err := runWork(t, a, "task", "show", "demo", "t1", "--json")
	if err != nil || strings.Contains(show, `"selfReported": true`) {
		t.Fatalf("CLI-run validator must not be marked self-reported: %v\n%s", err, show)
	}
	if !strings.Contains(show, `"submission": 1`) || !strings.Contains(show, `"exit": 0`) {
		t.Fatalf("validator record: %s", show)
	}

	// Findings, a negative result, and the brief.
	if _, err := runWork(t, a, "finding", "add", "demo", "the", "answer", "is", "42", "--source", "result.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runWork(t, a, "finding", "add", "demo", "guessing", "fails", "--negative"); err == nil {
		t.Fatal("negative result without conditions accepted")
	}
	if _, err := runWork(t, a, "finding", "add", "demo", "guessing", "fails", "--negative", "--conditions", "without reading the spec"); err != nil {
		t.Fatal(err)
	}
	as(a, "carol")
	if _, err := runWork(t, a, "finding", "verify", "demo", "f1", "re-read", "result.txt"); err != nil {
		t.Fatal(err)
	}
	brief, err := runWork(t, a, "brief", "demo")
	for _, want := range []string{"# demo — answer the question", "## Verified", "t1 [verified]", "## Verified findings", "f1 [verified]",
		"Failed experiments (avoid repeating)", "applies when: without reading the spec", "Continue with: bp work feed demo --after "} {
		if err != nil || !strings.Contains(brief, want) {
			t.Fatalf("brief missing %q: %v\n%s", want, err, brief)
		}
	}

	// Changed input: the brief and doctor flag the finding as stale.
	os.WriteFile("result.txt", []byte("41\n"), 0o600)
	doctor, err := runWork(t, a, "doctor", "demo")
	if err != nil || !strings.Contains(doctor, "finding-stale\tf1") || !strings.Contains(doctor, "verified-artifact-changed\tt1") {
		t.Fatalf("doctor: %v\n%s", err, doctor)
	}

	// Feed with a cursor.
	feed, err := runWork(t, a, "feed", "demo", "--kind", "finding", "--summary")
	if err != nil || !strings.Contains(feed, "finding f1 verified") {
		t.Fatalf("feed: %v\n%s", err, feed)
	}
}

func TestWorkCLIWritesNeedProvenIdentity(t *testing.T) {
	a := newRoomTestApp(t, "") // unverified
	if _, err := runWork(t, a, "project", "create", "demo", "--goal", "g"); err == nil || !strings.Contains(err.Error(), "proven identity") {
		t.Fatalf("unverified write: %v", err)
	}
	// Reads need no identity (an unknown project is just not found).
	if _, err := runWork(t, a, "brief", "demo"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("read: %v", err)
	}
	if out, err := runWork(t, a, "projects"); err != nil || out != "" {
		t.Fatalf("projects: %v %q", err, out)
	}
	if _, err := runWork(t, a, "task", "list", "demo", "--bogus", "x"); err == nil || !strings.Contains(err.Error(), "unknown option --bogus") {
		t.Fatalf("unknown flag: %v", err)
	}
	// "--help" after "--" belongs to the validator argv, not to bp.
	as(a, "x")
	if _, err := runWork(t, a, "task", "verify", "demo", "t1", "--run", "--", "tool", "--help"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("argv --help: %v", err)
	}
}
