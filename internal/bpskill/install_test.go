package bpskill

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallBothCLIsAndPreserveUserChanges(t *testing.T) {
	home := t.TempDir()
	if _, err := Install(home, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, cli := range []string{".codex", ".claude"} {
		path := filepath.Join(home, cli, "skills/blueprint/SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, Content) {
			t.Fatalf("missing bundled skill %s: %v", path, err)
		}
	}
	if _, err := Install(home, "", ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".codex/skills/blueprint/SKILL.md")
	if err := os.WriteFile(path, []byte("user-owned skill"), 0600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home, ".claude/skills/blueprint/SKILL.md")
	old := []byte("<!-- Managed by bp setup. -->\nuser edits to managed copy")
	if err := os.WriteFile(other, old, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(home, "", ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "user-owned skill" {
		t.Fatal("overwrote user skill")
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(other), "SKILL.md.before-bp-*"))
	if len(backups) != 1 {
		t.Fatalf("backups: %v", backups)
	}
	data, _ = os.ReadFile(backups[0])
	if !bytes.Equal(data, old) {
		t.Fatal("backup lost changes")
	}
}

func TestInstallRespectsNativeHomesAndSymlinkedUserSkill(t *testing.T) {
	home := t.TempDir()
	native := filepath.Join(home, "custom-native")
	if _, err := Install(home, native, native); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatal("ignored native home override")
	}
	target := filepath.Join(home, "owned.md")
	os.WriteFile(target, []byte("keep"), 0600)
	other := filepath.Join(home, "custom-claude/skills/blueprint")
	os.MkdirAll(other, 0700)
	os.Symlink(target, filepath.Join(other, "SKILL.md"))
	if _, err := Install(home, native, filepath.Join(home, "custom-claude")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "keep" {
		t.Fatal("modified symlink target")
	}
	info, _ := os.Lstat(filepath.Join(other, "SKILL.md"))
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("replaced user symlink")
	}
}
