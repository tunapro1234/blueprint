package config

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed example.yaml
var ExampleYAML []byte

// InitYAML creates a readable local config without replacing any existing format.
func InitYAML(home string) (string, error) {
	if home == "" {
		return "", os.ErrInvalid
	}
	for _, name := range []string{"config.yaml", "config.yml", "config.json"} {
		path := filepath.Join(home, name)
		if _, err := os.Lstat(path); err == nil {
			return path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(home, "config.yaml")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return path, nil
	}
	if err != nil {
		return "", err
	}
	_, writeErr := f.Write(ExampleYAML)
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	return path, closeErr
}

// HomeSentinel marks a directory bp created as its home. bp uninstall --purge
// deletes only a home that carries it, so a stray BP_HOME pointing at a
// project with its own config.json can never be wiped.
const HomeSentinel = ".bp-home"

// MarkHome writes the sentinel into home (idempotent).
func MarkHome(home string) error {
	path := filepath.Join(home, HomeSentinel)
	if _, err := os.Lstat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte("This directory is a bp home. bp uninstall --purge deletes it.\n"), 0600)
}

// IsMarkedHome reports whether home carries the sentinel as a regular file.
func IsMarkedHome(home string) bool {
	info, err := os.Lstat(filepath.Join(home, HomeSentinel))
	return err == nil && info.Mode().IsRegular()
}
