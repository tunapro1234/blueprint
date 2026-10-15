package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/config"
	"blueprint/internal/hop"
	"blueprint/internal/p2p"
	bptmux "blueprint/internal/tmux"
)

func TestHopHarnessAndFlag(t *testing.T) {
	cases := []struct {
		opt  *bptmux.OpenOptions
		want string
	}{
		{nil, "claude"},
		{&bptmux.OpenOptions{}, "claude"},
		{&bptmux.OpenOptions{Codex: true}, "codex"},
		{&bptmux.OpenOptions{Hermes: true}, "hermes"},
		{&bptmux.OpenOptions{OpenCode: true}, "opencode"},
	}
	for _, c := range cases {
		if got := hopHarness(c.opt); got != c.want {
			t.Fatalf("hopHarness=%q want %q", got, c.want)
		}
	}
	if f := harnessFlag("claude"); f != nil {
		t.Fatalf("claude needs no flag, got %v", f)
	}
	if f := harnessFlag("codex"); len(f) != 1 || f[0] != "--codex" {
		t.Fatalf("codex flag = %v", f)
	}
}

func TestHopLaunch(t *testing.T) {
	claude := hopLaunch("claude", "sess-123")
	if claude == nil || !claude.Resume || claude.ResumeID != "sess-123" || claude.Codex || claude.Hermes || claude.OpenCode {
		t.Fatalf("claude launch = %+v", claude)
	}
	// A claude agent with no recorded session still revives, just without a
	// resume target.
	bare := hopLaunch("claude", "")
	if bare == nil || !bare.Resume || bare.ResumeID != "" {
		t.Fatalf("bare claude launch = %+v", bare)
	}
	if o := hopLaunch("codex", "ignored"); o == nil || !o.Codex || o.Resume {
		t.Fatalf("codex launch = %+v", o)
	}
	if o := hopLaunch("hermes", ""); o == nil || !o.Hermes {
		t.Fatalf("hermes launch = %+v", o)
	}
	if o := hopLaunch("opencode", ""); o == nil || !o.OpenCode {
		t.Fatalf("opencode launch = %+v", o)
	}
}

// stageTreeBundle writes a three-agent subtree bundle into a fresh staging dir
// and returns the app, the staging id, and the staged meta, ready for accept.
func stageTreeBundle(t *testing.T, stateDir, bookPath string) (*app, string, p2p.HopMeta) {
	t.Helper()
	// root(claude) -> child(claude) -> grandchild(codex), each with one work
	// file; the root also carries a transcript.
	rootDir, childDir, grandDir := t.TempDir(), t.TempDir(), t.TempDir()
	for dir, name := range map[string]string{rootDir: "root.txt", childDir: "child.txt", grandDir: "grand.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+" body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	transcript := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return time.Unix(0, 0).UTC() }

	var buf bytes.Buffer
	if _, err := hop.WriteTree(&buf, hop.TreeWriteOptions{
		Root: "lh-hack",
		Agents: []hop.TreeWriteAgent{
			{Spec: hop.AgentSpec{Name: "lh-hack", Harness: "claude", SessionID: "sess", FolderBase: "lh-hack", Role: "lead", Color: "#ff0000"}, Folder: rootDir, IncludeFiles: true,
				Transcripts: []hop.TranscriptFile{{AbsPath: transcript, Name: "sess.jsonl"}}},
			{Spec: hop.AgentSpec{Name: "akbank-main", Harness: "claude", FolderBase: "akbank"}, Parent: "lh-hack", Folder: childDir, IncludeFiles: true},
			{Spec: hop.AgentSpec{Name: "akbank-main-dev", Harness: "codex", FolderBase: "dev"}, Parent: "akbank-main", Folder: grandDir, IncludeFiles: true},
		},
		Source: hop.Origin{Host: "server", Agent: "lh-hack"},
		Clock:  clock,
	}); err != nil {
		t.Fatal(err)
	}

	id := "p" + "00000000000000000000000000000001"
	// hopAcceptTree reads the bundle purely by meta.ID; the header is unused here.
	meta := p2p.HopMeta{ID: id, PeerAlias: "server", State: "pending"}
	// Stage the bundle on disk exactly where hopAcceptTree reads it.
	if err := os.MkdirAll(p2p.HopStagingDir(stateDir, id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p2p.HopBundlePath(stateDir, meta), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	// A tmux stub that reports no running session, so hopAcceptTree's
	// name-collision check passes with nothing live.
	tmuxBin := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(tmuxBin, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := &app{
		ctx:    context.Background(),
		config: config.Config{Agentbooks: []string{bookPath}, StateDir: stateDir},
		tmux:   &bptmux.Client{Bin: tmuxBin},
		out:    testOutput(t),
		err:    testOutput(t),
	}
	return a, id, meta
}

func TestHopAcceptTreeRoundTrip(t *testing.T) {
	t.Setenv("TMUX", "")
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	bookPath := filepath.Join(root, "agentbook.json")
	emptyBook, err := json.Marshal(book.File{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bookPath, emptyBook, 0o600); err != nil {
		t.Fatal(err)
	}
	destBase := filepath.Join(root, "landing")

	a, id, meta := stageTreeBundle(t, stateDir, bookPath)

	// Accept the whole subtree under a local parent, registered-not-launched.
	if err := a.hopAcceptTree(id, meta, destBase, "server-main", false, false); err != nil {
		t.Fatalf("hopAcceptTree: %v", err)
	}

	// Files landed under each agent's folder base.
	for rel, want := range map[string]string{
		"lh-hack/root.txt": "root.txt body",
		"akbank/child.txt": "child.txt body",
		"dev/grand.txt":    "grand.txt body",
	} {
		got, err := os.ReadFile(filepath.Join(destBase, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", rel, got, err, want)
		}
	}
	// The root's transcript landed in its Claude project dir.
	proj := bptmux.ClaudeProjectDir(filepath.Join(destBase, "lh-hack"))
	if got, err := os.ReadFile(filepath.Join(proj, "sess.jsonl")); err != nil || string(got) != `{"type":"user"}` {
		t.Fatalf("transcript = %q, %v", got, err)
	}

	// Hierarchy and registration: all closed, parents preserved, root reparented.
	fleet, err := book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lh-hack", "akbank-main", "akbank-main-dev"} {
		ag, ok := fleet.Agents[name]
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if ag.Status != "closed" {
			t.Fatalf("%s status = %q, want closed (must not auto-launch)", name, ag.Status)
		}
	}
	if p := fleet.Parents["lh-hack"]; p != "server-main" {
		t.Fatalf("root parent = %q, want server-main", p)
	}
	if p := fleet.Parents["akbank-main"]; p != "lh-hack" {
		t.Fatalf("akbank-main parent = %q, want lh-hack", p)
	}
	if p := fleet.Parents["akbank-main-dev"]; p != "akbank-main" {
		t.Fatalf("akbank-main-dev parent = %q, want akbank-main", p)
	}
	// Harness-specific launch preserved.
	if l := fleet.Agents["lh-hack"].Launch; l == nil || !l.Resume || l.ResumeID != "sess" {
		t.Fatalf("root launch = %+v", fleet.Agents["lh-hack"].Launch)
	}
	if l := fleet.Agents["akbank-main-dev"].Launch; l == nil || !l.Codex {
		t.Fatalf("codex dev launch = %+v", fleet.Agents["akbank-main-dev"].Launch)
	}
	if c := fleet.Agents["lh-hack"].Color; c != "#ff0000" {
		t.Fatalf("root color = %q, want #ff0000", c)
	}
	// Staging was cleared after a successful accept.
	if _, err := os.Stat(p2p.HopStagingDir(stateDir, id)); !os.IsNotExist(err) {
		t.Fatalf("staging dir should be gone, stat err = %v", err)
	}
}

func TestHopMode(t *testing.T) {
	if got := hopMode(true, false, true); got != "move,with-files" {
		t.Fatalf("got %q", got)
	}
	if got := hopMode(false, false, false); got != "copy,no-files" {
		t.Fatalf("got %q", got)
	}
	if got := hopMode(true, true, true); got != "move,delete-source,with-files" {
		t.Fatalf("got %q", got)
	}
}

func TestRemoveSourceFolderGuards(t *testing.T) {
	if err := removeSourceFolder("/"); err == nil {
		t.Fatal("must refuse /")
	}
	if home, _ := os.UserHomeDir(); home != "" {
		if err := removeSourceFolder(home); err == nil {
			t.Fatal("must refuse home")
		}
	}
	dir := filepath.Join(t.TempDir(), "victim")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeSourceFolder(dir); err != nil {
		t.Fatalf("remove ordinary dir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("dir should be gone")
	}
}

func TestMergeTree(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	for rel, content := range map[string]string{
		"a.txt":       "one",
		"sub/b.txt":   "two",
		"sub/c/d.txt": "three",
	} {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A pre-existing file in dst must survive the merge.
	if err := os.WriteFile(filepath.Join(dst, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeTree(src, dst); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"a.txt":       "one",
		"sub/b.txt":   "two",
		"sub/c/d.txt": "three",
		"keep.txt":    "keep",
	} {
		b, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("missing %s: %v", rel, err)
		}
		if string(b) != want {
			t.Fatalf("%s=%q want %q", rel, b, want)
		}
	}
	// A missing source is a no-op, not an error.
	if err := mergeTree(filepath.Join(src, "nope"), dst); err != nil {
		t.Fatalf("missing src should be a no-op: %v", err)
	}
}
