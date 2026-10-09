package bpskill

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"blueprint/internal/safefile"
)

//go:embed SKILL.md
var Content []byte

// Hint is the short, generic note a communicate-only install ships
// (docs/agent-hint.md). Content, the operational skill, is for installs that
// run agent sessions.
//
//go:embed HINT.md
var Hint []byte

// Marker identifies a skill file bp manages; files without it are the user's.
const Marker = "<!-- Managed by bp setup. -->"

// Install updates only bp-managed skills. Unmarked user skills and symlinks stay
// untouched; modified managed copies get a backup before replacement.
func Install(home, codexHome, claudeHome string) ([]string, error) {
	return InstallContent(home, codexHome, claudeHome, Content)
}

// InstallContent is Install with the skill text chosen by the caller.
func InstallContent(home, codexHome, claudeHome string, content []byte) ([]string, error) {
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
		old, snap, err := safefile.Read(path)
		if err != nil {
			return results, err
		}
		if snap.Exists {
			if bytes.Equal(old, content) {
				results = append(results, "skill ready: "+path)
				continue
			}
			if !bytes.Contains(old, []byte(Marker)) {
				results = append(results, "preserved user skill: "+path)
				continue
			}
			// Switching between the texts bp ships (hint and operational
			// skill) replaces bp's own file; only a user edit gets a backup.
			if shipped(old) {
				old = nil
			}
		}
		if old != nil {
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
			results = append(results, "skill backup: "+backup.Name())
		}
		if err := safefile.Replace(snap, content, 0600); err != nil {
			return results, fmt.Errorf("install skill %s: %w", path, err)
		}
		results = append(results, "skill installed: "+path)
	}
	return results, nil
}

func shipped(text []byte) bool {
	return bytes.Equal(text, Content) || bytes.Equal(text, Hint)
}
