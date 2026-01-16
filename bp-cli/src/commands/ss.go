package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	depsState, depWarnings, err := bpObj.DependencyState()
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	if !skipTests {
		if err := runVerificationCommands(bpObj); err != nil {
			return CommandResult{ExitCode: 1, Output: "Tests failed", Errors: []string{"Tests failed"}}
		}
	}

	files, err := bpObj.ComputeFileHashes()
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	blueprintHash, err := bp.HashFile(bpObj.Path)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	implHash := bp.ComputeImplHash(files)
	depsHash := bp.ComputeDepsHash(depsState)
	snapshotID := bp.ComputeSnapshotID(blueprintHash, implHash, depsHash)

	state := &bp.State{
		SnapshotID:    snapshotID,
		BlueprintHash: blueprintHash,
		ImplHash:      implHash,
		Files:         files,
		Deps:          depsState,
	}

	created, err := ensureSnapshotDir(bpObj.StateDir, snapshotID)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	if !created {
		if err := bpObj.SaveState(state); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		if err := os.WriteFile(filepath.Join(bpObj.StateDir, "current"), []byte(snapshotID), 0o644); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		out := formatSnapshotOutput(depWarnings, "Already exists")
		return CommandResult{
			ExitCode: 0,
			Output:   out,
			Data:     SnapshotInfo{ID: snapshotID, Path: snapshotDir(bpObj.StateDir, snapshotID), ImplHash: implHash, DepsHash: depsHash},
		}
	}

	if err := copyBlueprintSnapshot(bpObj.Path, snapshotBlueprintPath(bpObj.StateDir, snapshotID)); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := writeMeta(bpObj.StateDir, snapshotID, message, implHash, depsHash); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := bpObj.SaveState(state); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := os.WriteFile(filepath.Join(bpObj.StateDir, "current"), []byte(snapshotID), 0o644); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	out := formatSnapshotOutput(depWarnings, fmt.Sprintf("Snapshot #%s created", snapshotID))
	return CommandResult{
		ExitCode: 0,
		Output:   out,
		Data:     SnapshotInfo{ID: snapshotID, Path: snapshotDir(bpObj.StateDir, snapshotID), ImplHash: implHash, DepsHash: depsHash},
	}
}

func formatSnapshotOutput(warnings []string, main string) string {
	lines := []string{}
	for _, w := range warnings {
		if strings.TrimSpace(w) == "" {
			continue
		}
		lines = append(lines, "⚠ "+w)
	}
	if main != "" {
		lines = append(lines, main)
	}
	return strings.Join(lines, "\n")
}

func ensureSnapshotDir(stateDir, id string) (bool, error) {
	base := filepath.Join(stateDir, "history")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return false, err
	}
	target := filepath.Join(base, id)
	err := os.Mkdir(target, 0o755)
	if err == nil {
		return true, nil
	}
	if os.IsExist(err) {
		return false, nil
	}
	return false, err
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

func writeMeta(stateDir, id string, message any, implHash, depsHash string) error {
	meta := bp.HistoryEntry{
		ID:        id,
		Timestamp: time.Now().Format("2006-01-02T15:04:05"),
		Message:   resolveMessage(time.Now().Format("2006-01-02"), message),
		ImplHash:  implHash,
		DepsHash:  depsHash,
	}
	var b strings.Builder
	b.WriteString("id: ")
	b.WriteString(meta.ID)
	b.WriteString("\ntimestamp: ")
	b.WriteString(meta.Timestamp)
	b.WriteString("\nmessage: ")
	b.WriteString(meta.Message)
	b.WriteString("\nimpl_hash: ")
	b.WriteString(meta.ImplHash)
	b.WriteString("\ndeps_hash: ")
	b.WriteString(meta.DepsHash)
	b.WriteString("\n")
	metaPath := filepath.Join(stateDir, "history", id, "meta.yaml")
	return os.WriteFile(metaPath, []byte(b.String()), 0o644)
}
