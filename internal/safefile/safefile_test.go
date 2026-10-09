package safefile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplaceRefusesAConcurrentWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{}"), 0640); err != nil {
		t.Fatal(err)
	}
	_, snap, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	// Another program writes after bp read.
	later := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(path, []byte(`{"theirs": 1}`), 0640); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(path, later, later)
	if err := Replace(snap, []byte(`{"bp": 1}`), 0600); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"theirs": 1}` {
		t.Fatalf("lost the concurrent write: %s", data)
	}
}

func TestReplaceKeepsModeAndHardLinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rc")
	other := filepath.Join(dir, "rc-link")
	if err := os.WriteFile(path, []byte("a\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, other); err != nil {
		t.Fatal(err)
	}
	_, snap, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Replace(snap, []byte("b\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(other); string(data) != "b\n" {
		t.Fatalf("hard link split: %q", data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0640 {
		t.Fatalf("mode = %v", info.Mode())
	}
}

func TestReplaceNewFileRefusesIfCreatedMeanwhile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.json")
	_, snap, err := Read(path)
	if err != nil || snap.Exists {
		t.Fatalf("snap=%+v err=%v", snap, err)
	}
	if err := os.WriteFile(path, []byte("theirs"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Replace(snap, []byte("bp"), 0600); !errors.Is(err, ErrChanged) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckOwner(t *testing.T) {
	if err := CheckOwner(t.TempDir()); err != nil {
		t.Fatalf("own dir refused: %v", err)
	}
	if os.Geteuid() == 0 {
		dir := t.TempDir()
		if err := os.Chown(dir, 12345, 12345); err != nil {
			t.Fatal(err)
		}
		if err := CheckOwner(dir); err == nil {
			t.Fatal("root editing another user's home was not refused")
		}
		t.Setenv("BP_ALLOW_FOREIGN_HOME", "1")
		if err := CheckOwner(dir); err != nil {
			t.Fatal(err)
		}
	}
}
