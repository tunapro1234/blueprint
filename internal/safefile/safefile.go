// Package safefile edits files bp does not own (a user's rc file, a harness
// settings file, bp's own config while other tools may write it) without
// losing a concurrent write or the file's owner.
//
// The pattern is Read, compute the new bytes, Replace: Replace refuses with
// ErrChanged when the file changed after Read, keeps mode and (when running
// as root) owner, and keeps hard links by writing in place.
package safefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrChanged means the file changed between Read and Replace; nothing was
// written. The caller re-reads and retries, or reports it.
var ErrChanged = errors.New("the file changed while bp was editing it; nothing was written, try again")

// Snapshot is what Read saw.
type Snapshot struct {
	Path   string // the real file (symlinks resolved)
	Exists bool
	info   os.FileInfo
}

// Mode is the file's permission bits, or fallback when it does not exist.
func (s Snapshot) Mode(fallback os.FileMode) os.FileMode {
	if s.info == nil {
		return fallback
	}
	return s.info.Mode().Perm()
}

// Resolve returns the file a write to path must change: the symlink target
// for a link (so a dotfile manager's link stays a link). A dangling link is
// refused rather than replaced by a regular file.
func Resolve(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("%s is a symlink bp cannot follow (%v); not replacing it", path, err)
	}
	return real, nil
}

// Read reads path (through symlinks) and remembers its state.
func Read(path string) ([]byte, Snapshot, error) {
	real, err := Resolve(path)
	if err != nil {
		return nil, Snapshot{}, err
	}
	info, err := os.Stat(real)
	if errors.Is(err, os.ErrNotExist) {
		return nil, Snapshot{Path: real}, nil
	}
	if err != nil {
		return nil, Snapshot{}, err
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return nil, Snapshot{}, err
	}
	// A write between Stat and ReadFile shows up as a changed mtime/size.
	if after, err := os.Stat(real); err != nil || !same(info, after) {
		return nil, Snapshot{}, ErrChanged
	}
	return data, Snapshot{Path: real, Exists: true, info: info}, nil
}

func same(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// Replace writes data to the file Read saw, unless it changed since. mode is
// used for a new file; an existing file keeps its mode and owner.
func Replace(snap Snapshot, data []byte, mode os.FileMode) error {
	if snap.Path == "" {
		return errors.New("safefile: Replace without Read")
	}
	if err := unchanged(snap); err != nil {
		return err
	}
	if snap.Exists {
		mode = snap.info.Mode().Perm()
		if stat, ok := snap.info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
			// Renaming would split a hard link; write in place instead.
			return writeInPlace(snap.Path, data)
		}
	}
	dir := filepath.Dir(snap.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(snap.Path)+".tmp-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if err := keepOwner(file, snap, dir); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unchanged(snap); err != nil {
		return err
	}
	return os.Rename(name, snap.Path)
}

// Remove deletes the file Read saw, unless it changed since.
func Remove(snap Snapshot) error {
	if err := unchanged(snap); err != nil {
		return err
	}
	if !snap.Exists {
		return nil
	}
	return os.Remove(snap.Path)
}

func unchanged(snap Snapshot) error {
	info, err := os.Stat(snap.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if snap.Exists {
			return ErrChanged
		}
		return nil
	case err != nil:
		return err
	case !snap.Exists || !same(snap.info, info):
		return ErrChanged
	}
	return nil
}

// keepOwner gives the new file the old file's owner (or, for a new file, the
// directory's) when bp runs as root, so a sudo run never leaves a user's file
// owned by root.
func keepOwner(file *os.File, snap Snapshot, dir string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	source := snap.info
	if source == nil {
		var err error
		if source, err = os.Stat(dir); err != nil {
			return err
		}
	}
	stat, ok := source.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return file.Chown(int(stat.Uid), int(stat.Gid))
}

func writeInPlace(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// CheckOwner refuses to edit files under home when bp runs as a different
// user than the one owning it (typically sudo with HOME kept), unless the
// caller set BP_ALLOW_FOREIGN_HOME=1.
func CheckOwner(home string) error {
	if home == "" || os.Getenv("BP_ALLOW_FOREIGN_HOME") == "1" {
		return nil
	}
	info, err := os.Stat(home)
	if err != nil {
		return nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) == os.Geteuid() {
		return nil
	}
	return fmt.Errorf("bp runs as uid %d but %s belongs to uid %d; run bp as that user (or set BP_ALLOW_FOREIGN_HOME=1 if you mean it)", os.Geteuid(), home, stat.Uid)
}
