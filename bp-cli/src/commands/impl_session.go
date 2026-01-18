package commands

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const implLockFile = "impl.lock"

func implLockPath(stateDir string) string {
	return filepath.Join(stateDir, implLockFile)
}

func hasImplLock(stateDir string) (bool, error) {
	_, err := os.Stat(implLockPath(stateDir))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func writeImplLock(stateDir, snapshotID, mode string) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("status: active\n")
	if strings.TrimSpace(snapshotID) != "" {
		b.WriteString("snapshot_id: ")
		b.WriteString(snapshotID)
		b.WriteString("\n")
	}
	if strings.TrimSpace(mode) != "" {
		b.WriteString("mode: ")
		b.WriteString(mode)
		b.WriteString("\n")
	}
	b.WriteString("started: ")
	b.WriteString(time.Now().Format("2006-01-02T15:04:05"))
	b.WriteString("\n")
	return os.WriteFile(implLockPath(stateDir), []byte(b.String()), 0o644)
}

func clearImplLock(stateDir string) error {
	err := os.Remove(implLockPath(stateDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
