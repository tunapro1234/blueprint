package commands

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	bp "blueprint"
)

type depInfo struct {
	bp         *bp.Blueprint
	label      string
	snapshotID string
	apiHash    string
	rotten     bool
	relPath    string
	state      *bp.State
}

func ImplementCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
	}
	noSnapshot, _ := ctx.Args["no_snapshot"].(bool)
	bpObj, err := bp.LoadBlueprint(path)
	if err != nil {
		if errors.Is(err, bp.ErrNotBlueprint) {
			out := "Blueprint not found"
			return CommandResult{ExitCode: 2, Output: out, Errors: []string{out}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	validation := bpObj.Validate()
	if len(validation.Errors) > 0 {
		out := fmt.Sprintf("✗ %s: %s", formatPath(bpObj.Path), strings.Join(validation.Errors, "; "))
		return CommandResult{ExitCode: 2, Output: out, Errors: validation.Errors}
	}
	warnRottenDependencies(bpObj)
	mode := bpObj.Mode()
	active, err := hasImplLock(bpObj.StateDir)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if active {
		out := "Implementation already active."
		return CommandResult{ExitCode: 1, Output: out, Errors: []string{out}}
	}

	if err := writeImplLock(bpObj.StateDir, "", "compile"); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	locked := true
	trackedFiles := map[string]string{}
	cleanup := func() error {
		if !locked {
			return nil
		}
		locked = false
		return finalizeApply(bpObj, trackedFiles)
	}

	tree := &bp.BlueprintTree{Root: bpObj.Dir}
	deps, warnings, err := tree.ResolveDeps(bpObj)
	if err != nil {
		_ = cleanup()
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	warnLines := []string{}
	for _, w := range warnings {
		if strings.TrimSpace(w) == "" {
			continue
		}
		warnLines = append(warnLines, "⚠ "+w)
	}

	currentState := &bp.State{Deps: map[string]bp.DepState{}, Dependents: map[string]bp.DepRef{}, Files: map[string]string{}}
	if state, err := bp.LoadState(filepath.Join(bpObj.StateDir, "state.yaml")); err == nil {
		currentState = state
	} else if err != nil && !errors.Is(err, bp.ErrNoSnapshot) {
		_ = cleanup()
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if currentState.Deps == nil {
		currentState.Deps = map[string]bp.DepState{}
	}
	if currentState.Dependents == nil {
		currentState.Dependents = map[string]bp.DepRef{}
	}

	depLines := []string{}
	missing := []string{}
	depStates := []bp.DepState{}
	infos := []depInfo{}
	for _, dep := range deps {
		label := formatDepLabel(bpObj.Dir, dep.Dir)
		statePath := filepath.Join(dep.StateDir, "state.yaml")
		state, err := bp.LoadState(statePath)
		if err != nil {
			if errors.Is(err, bp.ErrNoSnapshot) {
				missing = append(missing, label)
				continue
			}
			_ = cleanup()
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		if strings.TrimSpace(state.SnapshotID) == "" {
			missing = append(missing, label)
			continue
		}
		snapshotID := state.SnapshotID
		apiHash, err := apiHashForSnapshot(dep, snapshotID)
		if err != nil {
			_ = cleanup()
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		rotten := false
		if meta, err := bp.LoadSnapshotMeta(dep.StateDir, snapshotID); err == nil {
			rotten = meta.Rotten
		}
		rel, err := filepath.Rel(dep.Dir, bpObj.Dir)
		if err != nil {
			_ = cleanup()
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = "."
		} else if !strings.HasPrefix(rel, ".") {
			rel = "./" + rel
		}
		depStates = append(depStates, bp.DepState{Pinned: snapshotID, Latest: snapshotID, APIHash: apiHash, LatestAPIHash: apiHash, APIChanged: false, Rotten: rotten})
		depLines = append(depLines, fmt.Sprintf("  %s @ %s ✓", label, snapshotID))
		infos = append(infos, depInfo{bp: dep, label: label, snapshotID: snapshotID, apiHash: apiHash, rotten: rotten, relPath: rel, state: state})
	}

	if len(missing) > 0 {
		errLines := make([]string, 0, len(missing))
		for _, dep := range missing {
			errLines = append(errLines, fmt.Sprintf("✗ Dependency %s has no snapshot. Run 'bp ss' in %s first.", dep, dep))
		}
		_ = cleanup()
		out := strings.Join(append(warnLines, errLines...), "\n")
		return CommandResult{
			ExitCode: 1,
			Output:   out,
			Errors:   errLines,
			Data:     ImplementResult{Compiled: false, NoSnapshot: noSnapshot, Deps: depStates, MissingDeps: missing},
		}
	}

	for _, info := range infos {
		if info.state.Dependents == nil {
			info.state.Dependents = map[string]bp.DepRef{}
		}
		info.state.Dependents[info.relPath] = bp.DepRef{Using: info.snapshotID}
		if err := info.bp.SaveState(info.state); err != nil {
			_ = cleanup()
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		currentState.Deps[info.label] = bp.DepState{
			Pinned:        info.snapshotID,
			Latest:        info.snapshotID,
			APIHash:       info.apiHash,
			LatestAPIHash: info.apiHash,
			APIChanged:    false,
			Rotten:        info.rotten,
		}
	}
	if err := bpObj.SaveState(currentState); err != nil {
		_ = cleanup()
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	if mode == "hide" {
		if currentID, err := readCurrentSnapshotID(bpObj.StateDir); err == nil {
			if err := restoreImplementationSnapshot(bpObj.StateDir, currentID, bpObj.Dir); err != nil {
				_ = cleanup()
				return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
			}
		} else if !errors.Is(err, bp.ErrNoSnapshot) && !errors.Is(err, ErrInvalidCurrentID) {
			_ = cleanup()
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
	}

	trackedFiles, err = bpObj.TrackedFiles()
	if err != nil {
		_ = cleanup()
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	if mode == "ro" {
		if err := setTrackedFilesWritable(trackedFiles); err != nil {
			_ = cleanup()
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
	}

	snapshotID := ""
	if !noSnapshot {
		snapCtx := CommandContext{Path: bpObj.Dir, Args: map[string]any{"message": "", "skip_tests": false}}
		snapResult := SsCommand(snapCtx)
		if snapResult.ExitCode != 0 {
			_ = cleanup()
			return CommandResult{
				ExitCode: snapResult.ExitCode,
				Output:   snapResult.Output,
				Errors:   snapResult.Errors,
				Data:     ImplementResult{Compiled: false, NoSnapshot: noSnapshot, Deps: depStates, MissingDeps: []string{}},
			}
		}
		if info, ok := snapResult.Data.(SnapshotInfo); ok {
			snapshotID = info.ID
		}
	}

	if err := cleanup(); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	lines := append([]string{}, warnLines...)
	if len(deps) > 0 {
		lines = append(lines, "Compiled:")
		lines = append(lines, depLines...)
	}
	if noSnapshot {
		lines = append(lines, "Compiled (no snapshot)")
	} else if len(deps) == 0 {
		lines = append(lines, "Compiled")
		lines = append(lines, fmt.Sprintf("Snapshot: %s", snapshotID))
	} else {
		lines = append(lines, fmt.Sprintf("Snapshot: %s", snapshotID))
	}
	return CommandResult{
		ExitCode: 0,
		Output:   strings.Join(lines, "\n"),
		Data:     ImplementResult{Compiled: true, SnapshotID: snapshotID, NoSnapshot: noSnapshot, Deps: depStates, MissingDeps: []string{}},
	}
}
