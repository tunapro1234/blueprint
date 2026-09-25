package release

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveReleaseReferencesUseCurrentHost(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	legacyHost := "bp." + "trasumanar.ai"
	var legacyReferences []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative == ".git" || relative == filepath.Join("site", "releases") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(legacyHost)) {
			legacyReferences = append(legacyReferences, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyReferences) > 0 {
		t.Fatalf("live files still reference the retired release host %s: %s", legacyHost, strings.Join(legacyReferences, ", "))
	}

	for index, relative := range []string{filepath.Join("..", "..", "install.sh"), filepath.Join("..", "..", "site", "install.sh")} {
		data, err := os.ReadFile(relative)
		if err != nil {
			if index == 1 && os.IsNotExist(err) {
				t.Log("site installer is not present in this sparse worktree")
				continue
			}
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte("local_base="+BaseURL)) {
			t.Errorf("%s does not use the release host %s", relative, BaseURL)
		}
	}
}
