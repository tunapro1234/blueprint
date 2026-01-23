package bp

import (
	"os"
	"path/filepath"
)

const implHiddenFile = "impl.hidden"

func implHiddenPath(stateDir string) string {
	return filepath.Join(stateDir, implHiddenFile)
}

func IsImplHidden(stateDir string) (bool, error) {
	_, err := os.Stat(implHiddenPath(stateDir))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func MarkImplHidden(stateDir string) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(implHiddenPath(stateDir), []byte("hidden: true\n"), 0o644)
}

func ClearImplHidden(stateDir string) error {
	err := os.Remove(implHiddenPath(stateDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
