package bp

import (
	"os"
	"path/filepath"
)

const implLockFileName = "impl.lock"

// HasImplLock reports whether impl.lock exists in the state directory.
func HasImplLock(stateDir string) (bool, error) {
	_, err := os.Stat(filepath.Join(stateDir, implLockFileName))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
