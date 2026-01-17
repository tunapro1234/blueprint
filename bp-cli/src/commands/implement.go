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
	relPath    string
	state      *bp.State
}

func ImplementCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
	}
	bpObj, err := bp.LoadBlueprint(path)
	if err != nil {
		if errors.Is(err, bp.ErrNotBlueprint) {
			out := "Blueprint not found"
			return CommandResult{ExitCode: 2, Output: out, Errors: []string{out}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	tree := &bp.BlueprintTree{Root: bpObj.Dir}
	deps, warnings, err := tree.ResolveDeps(bpObj)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	warnLines := []string{}
	for _, w := range warnings {
		if strings.TrimSpace(w) == "" {
			continue
		}
		warnLines = append(warnLines, "⚠ "+w)
	}

	if len(deps) == 0 {
		out := "No dependencies, ready to implement."
		if len(warnLines) > 0 {
			out = strings.Join(append(warnLines, out), "\n")
		}
		return CommandResult{
			ExitCode: 0,
			Output:   out,
			Data:     ImplementResult{Ready: true, Deps: []bp.DepState{}, MissingDeps: []string{}},
		}
	}

	currentState := &bp.State{Deps: map[string]bp.DepState{}, Dependents: map[string]bp.DepRef{}, Files: map[string]string{}}
	if state, err := bp.LoadState(filepath.Join(bpObj.StateDir, "state.yaml")); err == nil {
		currentState = state
	} else if err != nil && !errors.Is(err, bp.ErrNoSnapshot) {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if currentState.Deps == nil {
		currentState.Deps = map[string]bp.DepState{}
	}
	if currentState.Dependents == nil {
		currentState.Dependents = map[string]bp.DepRef{}
	}

	depLines := []string{"Dependencies:"}
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
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		if strings.TrimSpace(state.SnapshotID) == "" {
			missing = append(missing, label)
			continue
		}
		snapshotID := state.SnapshotID
		apiHash, err := apiHashForSnapshot(dep, snapshotID)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		rel, err := filepath.Rel(dep.Dir, bpObj.Dir)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = "."
		} else if !strings.HasPrefix(rel, ".") {
			rel = "./" + rel
		}
		depStates = append(depStates, bp.DepState{Pinned: snapshotID, Latest: snapshotID, APIHash: apiHash, LatestAPIHash: apiHash, APIChanged: false})
		depLines = append(depLines, fmt.Sprintf("  %s @ %s ✓", label, snapshotID))
		infos = append(infos, depInfo{bp: dep, label: label, snapshotID: snapshotID, apiHash: apiHash, relPath: rel, state: state})
	}

	if len(missing) > 0 {
		errLines := make([]string, 0, len(missing))
		for _, dep := range missing {
			errLines = append(errLines, fmt.Sprintf("✗ Dependency %s has no snapshot. Run 'bp ss' in %s first.", dep, dep))
		}
		out := strings.Join(append(warnLines, errLines...), "\n")
		return CommandResult{
			ExitCode: 1,
			Output:   out,
			Errors:   errLines,
			Data:     ImplementResult{Ready: false, Deps: depStates, MissingDeps: missing},
		}
	}

	for _, info := range infos {
		if info.state.Dependents == nil {
			info.state.Dependents = map[string]bp.DepRef{}
		}
		info.state.Dependents[info.relPath] = bp.DepRef{Using: info.snapshotID}
		if err := info.bp.SaveState(info.state); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		currentState.Deps[info.label] = bp.DepState{
			Pinned:        info.snapshotID,
			Latest:        info.snapshotID,
			APIHash:       info.apiHash,
			LatestAPIHash: info.apiHash,
			APIChanged:    false,
		}
	}
	if err := bpObj.SaveState(currentState); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	lines := append(warnLines, depLines...)
	lines = append(lines, "Ready to implement.")
	return CommandResult{
		ExitCode: 0,
		Output:   strings.Join(lines, "\n"),
		Data:     ImplementResult{Ready: true, Deps: depStates, MissingDeps: []string{}},
	}
}
