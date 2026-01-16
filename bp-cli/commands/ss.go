package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	bp "blueprint"
)

func SnapshotCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
	}
	message := ctx.Args["message"]
	skipTests, _ := ctx.Args["skip_tests"].(bool)
	bpObj, err := bp.LoadBlueprint(path)
	if err != nil {
		if errors.Is(err, bp.ErrNotBlueprint) {
			out := fmt.Sprintf("✗ %s: not a blueprint package", formatPath(path))
			return CommandResult{ExitCode: 1, Output: out, Errors: []string{"not a blueprint package"}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	validation := bpObj.Validate()
	if len(validation.Errors) > 0 {
		out := fmt.Sprintf("✗ %s: %s", formatPath(bpObj.Path), strings.Join(validation.Errors, "; "))
		return CommandResult{ExitCode: 2, Output: out, Errors: validation.Errors}
	}
	if len(validation.Warnings) > 0 {
		_ = validation.Warnings
	}
	if !skipTests {
		if err := runVerificationCommands(bpObj); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
	}
	id, err := nextSnapshotID(bpObj.StateDir)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	finalID, err := createSnapshotDir(bpObj.StateDir, id)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := copyBlueprintSnapshot(bpObj.Path, snapshotBlueprintPath(bpObj.StateDir, finalID)); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := writeMeta(bpObj.StateDir, finalID, message); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := bpObj.SaveState(); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := os.WriteFile(filepath.Join(bpObj.StateDir, "current"), []byte(finalID), 0o644); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	return CommandResult{ExitCode: 0, Output: fmt.Sprintf("Snapshot #%s created", finalID), Data: SnapshotInfo{ID: finalID, Path: snapshotDir(bpObj.StateDir, finalID)}}
}

func runVerificationCommands(bpObj *bp.Blueprint) error {
	section, err := bpObj.GetSection("tests")
	if err != nil {
		return err
	}
	if section == nil {
		return nil
	}
	verif, ok := section["verification"]
	if !ok || verif == nil {
		return nil
	}
	list, ok := verif.([]interface{})
	if !ok {
		return fmt.Errorf("tests.verification must be a list")
	}
	for _, raw := range list {
		cmdStr, ok := raw.(string)
		if !ok {
			return fmt.Errorf("tests.verification must be a list of strings")
		}
		if strings.TrimSpace(cmdStr) == "" {
			continue
		}
		if err := runCommand(cmdStr, bpObj.Dir, 5*time.Minute); err != nil {
			return fmt.Errorf("tests.verification failed: %s", cmdStr)
		}
	}
	return nil
}

func runCommand(cmdStr, cwd string, timeout time.Duration) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", cmdStr)
	} else {
		cmd = exec.Command("sh", "-c", cmdStr)
	}
	cmd.Dir = cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return runWithTimeout(cmd, timeout)
}

func runWithTimeout(cmd *exec.Cmd, timeout time.Duration) error {
	if timeout <= 0 {
		return cmd.Run()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return fmt.Errorf("command timed out")
	}
}

func nextSnapshotID(stateDir string) (string, error) {
	historyDir := filepath.Join(stateDir, "history")
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "0001", nil
		}
		return "", err
	}
	maxID := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isSnapshotID(name) {
			continue
		}
		val, _ := strconv.Atoi(name)
		if val > maxID {
			maxID = val
		}
	}
	next := maxID + 1
	if next > 9999 {
		return "", fmt.Errorf("Snapshot limit exceeded")
	}
	return fmt.Sprintf("%04d", next), nil
}

func createSnapshotDir(stateDir, id string) (string, error) {
	base := filepath.Join(stateDir, "history")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	for i := 0; i < 3; i++ {
		target := filepath.Join(base, id)
		err := os.Mkdir(target, 0o755)
		if err == nil {
			return id, nil
		}
		if os.IsExist(err) {
			// try next id
			val, _ := strconv.Atoi(id)
			val++
			if val > 9999 {
				return "", fmt.Errorf("Snapshot limit exceeded")
			}
			id = fmt.Sprintf("%04d", val)
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("Snapshot ID collision")
}

func copyBlueprintSnapshot(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return dstFile.Sync()
}

func writeMeta(stateDir, id string, message any) error {
	meta := bp.HistoryEntry{
		ID:        id,
		Timestamp: time.Now().Format("2006-01-02T15:04:05"),
		Message:   resolveMessage(time.Now().Format("2006-01-02"), message),
	}
	var b strings.Builder
	b.WriteString("id: ")
	b.WriteString(meta.ID)
	b.WriteString("\ntimestamp: ")
	b.WriteString(meta.Timestamp)
	b.WriteString("\nmessage: ")
	b.WriteString(meta.Message)
	b.WriteString("\n")
	metaPath := filepath.Join(stateDir, "history", id, "meta.yaml")
	return os.WriteFile(metaPath, []byte(b.String()), 0o644)
}
