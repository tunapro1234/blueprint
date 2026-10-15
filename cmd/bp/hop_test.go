package main

import (
	"os"
	"path/filepath"
	"testing"

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
