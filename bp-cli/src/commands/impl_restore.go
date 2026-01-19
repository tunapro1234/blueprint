package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	bp "blueprint"
)

func resolveImplementationSnapshotID(stateDir, input string) (string, error) {
	id := strings.TrimSpace(input)
	if id == "" || strings.EqualFold(id, "current") {
		current, err := readCurrentSnapshotID(stateDir)
		if err != nil {
			if errors.Is(err, bp.ErrNoSnapshot) {
				return "", fmt.Errorf("No snapshot history. Run 'bp apply' first or use 'bp impl clean'.")
			}
			return "", err
		}
		id = current
	}
	if !bp.IsSnapshotID(id) {
		return "", fmt.Errorf("Invalid snapshot ID: %s", id)
	}
	if _, err := os.Stat(snapshotDir(stateDir, id)); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("Snapshot #%s not found", id)
		}
		return "", err
	}
	return id, nil
}

func restoreImplementationSnapshot(stateDir, snapshotID, targetDir string) error {
	srcRoot := filepath.Join(stateDir, "history", snapshotID, "impl")
	info, err := os.Stat(srcRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("Snapshot #%s has no impl copy", snapshotID)
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("Snapshot #%s has no impl copy", snapshotID)
	}
	return filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if rel == "deps" || strings.HasPrefix(filepath.ToSlash(rel), "deps/") {
			return nil
		}
		dst := filepath.Join(targetDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return copySnapshotFile(path, dst)
	})
}

func copySnapshotFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	perm := info.Mode().Perm() | 0o200
	if perm == 0 {
		perm = 0o600
	}
	return os.Chmod(dst, perm)
}
