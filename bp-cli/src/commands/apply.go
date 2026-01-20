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

func SsCommand(ctx CommandContext) CommandResult {
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
	warnRottenDependencies(bpObj)
	validation := bpObj.Validate()
	if len(validation.Errors) > 0 {
		out := fmt.Sprintf("✗ %s: %s", formatPath(bpObj.Path), strings.Join(validation.Errors, "; "))
		return CommandResult{ExitCode: 2, Output: out, Errors: validation.Errors}
	}

	testCfg, err := readTestConfig(bpObj)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	testCfg.SnapshotReadOnly = true

	depsState, depWarnings, err := bpObj.DependencyState()
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	upgradeWarnings := formatDepUpgradeWarnings(depsState)
	tree := &bp.BlueprintTree{Root: bpObj.Dir}
	deps, _, err := tree.ResolveDeps(bpObj)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	if !skipTests {
		if err := runBlueprintTests(bpObj, testCfg); err != nil {
			return CommandResult{ExitCode: 1, Output: "Tests failed", Errors: []string{"Tests failed"}}
		}
	}

	trackedFiles, err := bpObj.TrackedFiles()
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
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
	apiHash, err := bpObj.APIHash()
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	specHash, err := bpObj.SpecHash()
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	contentHash := bp.ComputeContentHash(blueprintHash, implHash, depsHash)
	now := time.Now()
	messageText := ""
	if raw, ok := message.(string); ok {
		messageText = strings.TrimSpace(raw)
	}
	metaMessage := messageText
	if metaMessage == "" {
		metaMessage = now.Format("2006-01-02")
	}
	snapshotID := bp.BuildSnapshotID(contentHash, messageText)

	dependents := map[string]bp.DepRef{}
	if existing, err := bp.LoadState(filepath.Join(bpObj.StateDir, "state.yaml")); err == nil {
		if existing.Dependents != nil {
			dependents = existing.Dependents
		}
	}

	state := &bp.State{
		SnapshotID:    snapshotID,
		BlueprintHash: blueprintHash,
		ImplHash:      implHash,
		Files:         files,
		Deps:          depsState,
		Dependents:    dependents,
	}

	created, err := ensureSnapshotDir(bpObj.StateDir, snapshotID)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	warnings := append(depWarnings, upgradeWarnings...)

	if !created {
		match, err := snapshotMetaMatches(bpObj.StateDir, snapshotID, contentHash)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		if !match {
			msg := fmt.Sprintf("snapshot id collision: %s", snapshotID)
			return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
		}
		if err := bpObj.SaveState(state); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		if err := os.WriteFile(filepath.Join(bpObj.StateDir, "current"), []byte(snapshotID), 0o644); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		out := formatSnapshotOutput(warnings, fmt.Sprintf("Snapshot already exists: %s", snapshotID))
		if err := ensureDepsSymlinks(bpObj, deps, depsState, filepath.Join(bpObj.StateDir, "history", snapshotID), false); err != nil {
			msg := fmt.Sprintf("Failed to create dependency symlinks: %s", err.Error())
			out = strings.Join([]string{out, "✗ " + msg}, "\n")
			return CommandResult{
				ExitCode: 1,
				Output:   out,
				Errors:   []string{msg},
				Data:     SnapshotInfo{ID: snapshotID, Path: snapshotDir(bpObj.StateDir, snapshotID), ContentHash: contentHash, APIHash: apiHash, SpecHash: specHash, ImplHash: implHash},
			}
		}
		return CommandResult{
			ExitCode: 0,
			Output:   out,
			Data:     SnapshotInfo{ID: snapshotID, Path: snapshotDir(bpObj.StateDir, snapshotID), ContentHash: contentHash, APIHash: apiHash, SpecHash: specHash, ImplHash: implHash},
		}
	}

	if err := copyBlueprintSnapshot(bpObj.Path, snapshotBlueprintPath(bpObj.StateDir, snapshotID)); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := writeMeta(bpObj.StateDir, snapshotID, metaMessage, contentHash, apiHash, specHash, implHash, now, false); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := copyImplementationSnapshot(trackedFiles, bpObj.StateDir, snapshotID, testCfg.SnapshotReadOnly); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := ensureDepsSymlinks(bpObj, deps, depsState, filepath.Join(bpObj.StateDir, "history", snapshotID), false); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := bpObj.SaveState(state); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := os.WriteFile(filepath.Join(bpObj.StateDir, "current"), []byte(snapshotID), 0o644); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	out := formatSnapshotOutput(warnings, fmt.Sprintf("Snapshot created: %s", snapshotID))
	return CommandResult{
		ExitCode: 0,
		Output:   out,
		Data:     SnapshotInfo{ID: snapshotID, Path: snapshotDir(bpObj.StateDir, snapshotID), ContentHash: contentHash, APIHash: apiHash, SpecHash: specHash, ImplHash: implHash},
	}
}

func cleanImplementationFiles(files map[string]string) error {
	for _, abs := range files {
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func finalizeApply(bpObj *bp.Blueprint, trackedFiles map[string]string) error {
	switch bpObj.Mode() {
	case "hide":
		if err := cleanImplementationFiles(trackedFiles); err != nil {
			return fmt.Errorf("Failed to clean implementation files: %s", err.Error())
		}
	case "ro":
		if err := setTrackedFilesReadOnly(trackedFiles); err != nil {
			return fmt.Errorf("Failed to set files read-only: %s", err.Error())
		}
	}
	if err := clearImplLock(bpObj.StateDir); err != nil {
		return fmt.Errorf("Failed to clear implementation lock: %s", err.Error())
	}
	return nil
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

func snapshotMetaMatches(stateDir, id, contentHash string) (bool, error) {
	meta, err := bp.LoadSnapshotMeta(stateDir, id)
	if err != nil {
		return false, err
	}
	if meta.ContentHash == "" {
		return false, nil
	}
	return meta.ContentHash == contentHash, nil
}

type testConfig struct {
	Packages            []string
	DependencySnapshots bool
	SnapshotReadOnly    bool
}

var goTestRunner = runGoTest

func runBlueprintTests(bpObj *bp.Blueprint, cfg testConfig) error {
	if err := runPackageTests(bpObj.Dir, cfg.Packages); err != nil {
		return err
	}
	if err := runVerificationCommands(bpObj); err != nil {
		return err
	}
	if cfg.DependencySnapshots {
		if err := runDependencySnapshotTests(bpObj); err != nil {
			return err
		}
	}
	return nil
}

func readTestConfig(bpObj *bp.Blueprint) (testConfig, error) {
	cfg := testConfig{SnapshotReadOnly: true}
	section, err := bpObj.GetSection("tests")
	if err != nil {
		return cfg, err
	}
	if section == nil {
		return cfg, nil
	}
	if raw, ok := section["packages"]; ok && raw != nil {
		list, err := parseStringList(raw, "tests.packages")
		if err != nil {
			return cfg, err
		}
		cfg.Packages = list
	}
	if raw, ok := section["dependency_snapshots"]; ok && raw != nil {
		val, ok := raw.(bool)
		if !ok {
			return cfg, fmt.Errorf("tests.dependency_snapshots must be a boolean")
		}
		cfg.DependencySnapshots = val
	}
	if raw, ok := section["snapshot_readonly"]; ok && raw != nil {
		val, ok := raw.(bool)
		if !ok {
			return cfg, fmt.Errorf("tests.snapshot_readonly must be a boolean")
		}
		cfg.SnapshotReadOnly = val
	}
	return cfg, nil
}

func parseStringList(raw interface{}, field string) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case []string:
		return v, nil
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a list of strings", field)
			}
			out = append(out, str)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be a list", field)
	}
}

func runPackageTests(workDir string, packages []string) error {
	if len(packages) == 0 {
		return nil
	}
	return goTestRunner(workDir, packages)
}

func runGoTest(workDir string, packages []string) error {
	args := append([]string{"test"}, packages...)
	cmd := exec.Command("go", args...)
	cmd.Dir = workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return runWithTimeout(cmd, 10*time.Minute)
}

func runDependencySnapshotTests(bpObj *bp.Blueprint) error {
	tree := &bp.BlueprintTree{Root: bpObj.Dir}
	deps, _, err := tree.ResolveDeps(bpObj)
	if err != nil {
		return err
	}
	if len(deps) == 0 {
		return nil
	}
	var state *bp.State
	if loaded, err := bp.LoadState(filepath.Join(bpObj.StateDir, "state.yaml")); err == nil {
		state = loaded
	} else if err != nil && !errors.Is(err, bp.ErrNoSnapshot) {
		return err
	}
	for _, dep := range deps {
		label, err := relativeLabel(bpObj.Dir, dep.Dir)
		if err != nil {
			return err
		}
		id := ""
		if state != nil {
			if depState, ok := state.Deps[label]; ok {
				if depState.Pinned != "" {
					id = depState.Pinned
				} else if depState.Latest != "" {
					id = depState.Latest
				}
			}
		}
		if id == "" {
			currentID, err := readCurrentSnapshotID(dep.StateDir)
			if err != nil {
				return fmt.Errorf("dependency %s has no snapshot", label)
			}
			id = currentID
		}
		snapshotRoot := filepath.Join(dep.StateDir, "history", id)
		if _, err := os.Stat(snapshotRoot); err != nil {
			return err
		}
		depCfg, err := readTestConfig(dep)
		if err != nil {
			return err
		}
		if len(depCfg.Packages) == 0 {
			return fmt.Errorf("tests.packages missing for dependency %s", label)
		}
		if err := runPackageTests(snapshotRoot, depCfg.Packages); err != nil {
			return err
		}
	}
	return nil
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
	list, err := parseStringList(verif, "tests.verification")
	if err != nil {
		return err
	}
	for _, cmdStr := range list {
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

func copyImplementationSnapshot(files map[string]string, stateDir, id string, readOnly bool) error {
	base := filepath.Join(stateDir, "history", id)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	for rel, abs := range files {
		if rel == "BLUEPRINT.yaml" || rel == "meta.yaml" {
			return fmt.Errorf("reserved snapshot filename: %s", rel)
		}
		dst := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		srcInfo, err := os.Stat(abs)
		if err != nil {
			return err
		}
		srcFile, err := os.Open(abs)
		if err != nil {
			return err
		}
		dstFile, err := os.Create(dst)
		if err != nil {
			srcFile.Close()
			return err
		}
		if _, err := io.Copy(dstFile, srcFile); err != nil {
			srcFile.Close()
			dstFile.Close()
			return err
		}
		srcFile.Close()
		if err := dstFile.Sync(); err != nil {
			dstFile.Close()
			return err
		}
		if err := dstFile.Close(); err != nil {
			return err
		}
		if readOnly {
			if err := makeReadOnly(dst, srcInfo.Mode().Perm()); err != nil {
				return err
			}
		}
	}
	return nil
}

func makeReadOnly(path string, srcPerm os.FileMode) error {
	perm := srcPerm &^ 0o222
	if perm == 0 {
		perm = 0o444
	}
	return os.Chmod(path, perm)
}

func writeMeta(stateDir, id, message, contentHash, apiHash, specHash, implHash string, ts time.Time, rotten bool) error {
	meta := bp.HistoryEntry{
		ID:          id,
		Timestamp:   ts.Format("2006-01-02T15:04:05"),
		Message:     message,
		ContentHash: contentHash,
		APIHash:     apiHash,
		SpecHash:    specHash,
		ImplHash:    implHash,
		Rotten:      rotten,
	}
	return writeSnapshotMeta(stateDir, meta)
}

func writeSnapshotMeta(stateDir string, meta bp.HistoryEntry) error {
	var b strings.Builder
	b.WriteString("id: ")
	b.WriteString(meta.ID)
	b.WriteString("\ntimestamp: ")
	b.WriteString(meta.Timestamp)
	b.WriteString("\nmessage: ")
	b.WriteString(meta.Message)
	b.WriteString("\ncontent_hash: ")
	b.WriteString(meta.ContentHash)
	b.WriteString("\napi_hash: ")
	b.WriteString(meta.APIHash)
	b.WriteString("\nspec_hash: ")
	b.WriteString(meta.SpecHash)
	b.WriteString("\nimpl_hash: ")
	b.WriteString(meta.ImplHash)
	b.WriteString("\nrotten: ")
	b.WriteString(strconv.FormatBool(meta.Rotten))
	b.WriteString("\n")
	metaPath := filepath.Join(stateDir, "history", meta.ID, "meta.yaml")
	return os.WriteFile(metaPath, []byte(b.String()), 0o644)
}

func setSnapshotRotten(stateDir, id string, rotten bool) error {
	meta, err := bp.LoadSnapshotMeta(stateDir, id)
	if err != nil {
		return err
	}
	meta.Rotten = rotten
	return writeSnapshotMeta(stateDir, meta)
}
