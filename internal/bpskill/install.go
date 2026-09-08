package bpskill

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed SKILL.md
var Content []byte

// Install updates only bp-managed skills. Unmarked user skills and symlinks stay
// untouched; modified managed copies get a backup before replacement.
func Install(home, codexHome, claudeHome string) ([]string, error) {
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	if claudeHome == "" {
		claudeHome = filepath.Join(home, ".claude")
	}
	var results []string
	seen := map[string]bool{}
	for _, root := range []string{codexHome, claudeHome} {
		dir := filepath.Join(root, "skills", "blueprint")
		path := filepath.Join(dir, "SKILL.md")
		if seen[path] {
			continue
		}
		seen[path] = true
		if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
			results = append(results, "preserved user skill: "+path)
			continue
		}
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			results = append(results, "preserved user skill: "+path)
			continue
		}
		old, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return results, err
		}
		if err == nil {
			if bytes.Equal(old, Content) {
				results = append(results, "skill ready: "+path)
				continue
			}
			if !bytes.Contains(old, []byte("<!-- Managed by bp setup. -->")) {
				results = append(results, "preserved user skill: "+path)
				continue
			}
			backup, err := os.CreateTemp(dir, "SKILL.md.before-bp-*")
			if err != nil {
				return results, err
			}
			_, writeErr := backup.Write(old)
			closeErr := backup.Close()
			if writeErr != nil {
				return results, writeErr
			}
			if closeErr != nil {
				return results, closeErr
			}
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return results, err
		}
		f, err := os.CreateTemp(dir, ".skill-*")
		if err != nil {
			return results, err
		}
		_, err = f.Write(Content)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return results, fmt.Errorf("install skill %s: %w", path, err)
		}
		if err := os.Rename(f.Name(), path); err != nil {
			return results, err
		}
		results = append(results, "skill installed: "+path)
	}
	return results, nil
}
