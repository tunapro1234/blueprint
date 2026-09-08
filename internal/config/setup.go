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
