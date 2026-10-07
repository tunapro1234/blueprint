package claudeacct

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	secretFileMode = 0o600
	secretDirMode  = 0o700
)

// ensurePrivateDir creates dir (and parents) and tightens dir itself to 0700.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, secretDirMode); err != nil {
		return err
	}
	return os.Chmod(dir, secretDirMode)
}

// writeAtomic replaces path with data: temp file in the same directory, mode
// set before any byte is written, fsync, rename, directory fsync. Readers see
// either the old or the new file, never a torn one.
func writeAtomic(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".bp-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if err = tmp.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	if d, openErr := os.Open(dir); openErr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// fileSnapshot is the in-memory copy a switch rolls back to.
type fileSnapshot struct {
	path   string
	exists bool
	data   []byte
	mode   os.FileMode
}

func snapshotFile(path string) (fileSnapshot, error) {
	snap := fileSnapshot{path: path, mode: secretFileMode}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return snap, nil
	}
	if err != nil {
		return snap, err
	}
	if !info.Mode().IsRegular() {
		return snap, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return snap, err
	}
	snap.exists, snap.data, snap.mode = true, data, info.Mode().Perm()
	return snap, nil
}
