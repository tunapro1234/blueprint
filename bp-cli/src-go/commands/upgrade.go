package commands

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	bp "blueprint"
)

func UpgradeCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
	}
	depPath, _ := ctx.Args["dep_path"].(string)
	toVersion, _ := ctx.Args["to"].(string)
	safe, _ := ctx.Args["safe"].(bool)
	all, _ := ctx.Args["all"].(bool)
	force, _ := ctx.Args["force"].(bool)

	if toVersion != "" && depPath == "" {
		msg := "--to requires a specific dependency (dep_path)"
		return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
	}
	bpObj, err := bp.LoadBlueprint(path)
	if err != nil {
		if errors.Is(err, bp.ErrNotBlueprint) {
			out := fmt.Sprintf("✗ %s: not a blueprint package", formatPath(path))
			return CommandResult{ExitCode: 1, Output: out, Errors: []string{"not a blueprint package"}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	warnRottenDependencies(bpObj)
	state, err := bp.LoadState(filepath.Join(bpObj.StateDir, "state.yaml"))
	if err != nil {
		if errors.Is(err, bp.ErrNoSnapshot) {
			return CommandResult{ExitCode: 0, Output: "No upgrades available", Data: UpgradeResult{}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if len(state.Deps) == 0 {
		return CommandResult{ExitCode: 0, Output: "No upgrades available", Data: UpgradeResult{}}
	}

	testCfg, err := readTestConfig(bpObj)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	depsTree := &bp.BlueprintTree{Root: bpObj.Dir}
	depsList, _, err := depsTree.ResolveDeps(bpObj)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	depIndex := map[string]*bp.Blueprint{}
	for _, dep := range depsList {
		label := formatDepLabel(bpObj.Dir, dep.Dir)
		depIndex[label] = dep
	}

	targets := []string{}
	if depPath != "" {
		key := normalizeDepKey(depPath)
		if _, ok := state.Deps[key]; !ok {
			msg := fmt.Sprintf("Dependency not found: %s", depPath)
			return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
		}
		targets = []string{key}
	} else if all {
		for k := range state.Deps {
			targets = append(targets, k)
		}
		sort.Strings(targets)
	} else {
		for k := range state.Deps {
			targets = append(targets, k)
		}
		sort.Strings(targets)
	}

	upgraded := []string{}
	apiChangedList := []string{}
	skipped := []string{}
	skippedRotten := []string{}
	skippedAPI := []string{}
	failed := []string{}
	lines := []string{}
	dirty := false

	for _, key := range targets {
		depState := state.Deps[key]
		depBp, ok := depIndex[key]
		if !ok {
			depDir := filepath.Join(bpObj.Dir, filepath.FromSlash(key))
			depDir = filepath.Clean(depDir)
			bpPath, err := bp.FindBlueprintFile(depDir)
			if err != nil {
				msg := fmt.Sprintf("Dependency not found: %s", key)
				return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
			}
			depBp, err = bp.LoadBlueprint(bpPath)
			if err != nil {
				return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
			}
			depIndex[key] = depBp
		}

		// Determine target snapshot ID
		var targetID string
		if toVersion != "" {
			targetID, err = resolveSnapshotID(depBp.StateDir, toVersion)
			if err != nil {
				return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
			}
		} else {
			targetID, err = readCurrentSnapshotID(depBp.StateDir)
			if err != nil {
				return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
			}
		}
		current := depState.Pinned
		if current == "" {
			current = depState.Latest
		}
		targetAPI := ""
		targetRotten := false
		meta, err := bp.LoadSnapshotMeta(depBp.StateDir, targetID)
		if err == nil {
			targetAPI = meta.APIHash
			targetRotten = meta.Rotten
		}
		if strings.TrimSpace(targetAPI) == "" {
			targetAPI, err = depBp.APIHash()
			if err != nil {
				return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
			}
		}
		apiChanged := depState.APIHash != "" && targetAPI != "" && depState.APIHash != targetAPI

		depState.Latest = targetID
		depState.LatestAPIHash = targetAPI
		depState.APIChanged = apiChanged
		depState.Rotten = targetRotten
		state.Deps[key] = depState
		dirty = true

		if targetRotten && !force {
			skipped = append(skipped, key)
			skippedRotten = append(skippedRotten, key)
			lines = append(lines, fmt.Sprintf("Skipped %s: rotten (use --force)", key))
			continue
		}
		if apiChanged && safe {
			skipped = append(skipped, key)
			skippedAPI = append(skippedAPI, key)
			lines = append(lines, fmt.Sprintf("Skipped %s (API changed)", key))
			continue
		}
		if current == targetID {
			if toVersion != "" {
				lines = append(lines, fmt.Sprintf("Already at %s: %s", targetID, key))
			}
			continue
		}

		prevPinned := depState.Pinned
		prevAPIHash := depState.APIHash
		depState.Pinned = targetID
		depState.APIHash = targetAPI
		depState.APIChanged = false
		state.Deps[key] = depState
		if err := bpObj.SaveState(state); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}

		if err := runBlueprintTests(bpObj, testCfg); err != nil {
			_ = setSnapshotRotten(depBp.StateDir, targetID, true)
			depState.Pinned = prevPinned
			depState.APIHash = prevAPIHash
			depState.APIChanged = apiChanged
			depState.Rotten = true
			state.Deps[key] = depState
			_ = bpObj.SaveState(state)
			failed = append(failed, key)
			lines = append(lines, fmt.Sprintf("Failed %s: tests failed, rolled back, marked rotten", key))
			continue
		}

		depState.Latest = targetID
		depState.LatestAPIHash = targetAPI
		depState.APIChanged = false
		depState.Rotten = targetRotten
		state.Deps[key] = depState
		upgraded = append(upgraded, key)
		if toVersion != "" {
			lines = append(lines, fmt.Sprintf("Pinned %s: %s → %s", key, current, targetID))
			if apiChanged {
				apiChangedList = append(apiChangedList, key)
				lines = append(lines, fmt.Sprintf("  ⚠ API changed! Review: bp diff %s %s %s", key, current, targetID))
			}
		} else if apiChanged {
			apiChangedList = append(apiChangedList, key)
			lines = append(lines, fmt.Sprintf("Upgraded %s: %s → %s (API CHANGED!)", key, current, targetID))
			lines = append(lines, fmt.Sprintf("  Review: bp diff %s %s %s", key, current, targetID))
		} else if targetRotten && force {
			lines = append(lines, fmt.Sprintf("Upgraded %s (forced): %s → %s [ROTTEN]", key, current, targetID))
			lines = append(lines, "⚠ Warning: using rotten snapshot")
		} else {
			lines = append(lines, fmt.Sprintf("Upgraded %s: %s → %s", key, current, targetID))
		}

		depStateObj, err := bp.LoadState(filepath.Join(depBp.StateDir, "state.yaml"))
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		if depStateObj.Dependents == nil {
			depStateObj.Dependents = map[string]bp.DepRef{}
		}
		rel, err := relativeLabel(depBp.Dir, bpObj.Dir)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		depStateObj.Dependents[rel] = bp.DepRef{Using: targetID}
		if err := depBp.SaveState(depStateObj); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
	}

	if dirty {
		if err := bpObj.SaveState(state); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
	}

	if len(upgraded) == 0 && len(skipped) == 0 && len(failed) == 0 {
		if len(lines) > 0 {
			return CommandResult{ExitCode: 0, Output: strings.Join(lines, "\n"), Data: UpgradeResult{}}
		}
		return CommandResult{ExitCode: 0, Output: "No upgrades available", Data: UpgradeResult{}}
	}

	exitCode := 0
	if len(upgraded) == 0 && len(failed) > 0 {
		exitCode = 1
	}

	return CommandResult{
		ExitCode: exitCode,
		Output:   strings.Join(lines, "\n"),
		Data: UpgradeResult{
			Upgraded:      upgraded,
			APIChanged:    apiChangedList,
			Skipped:       skipped,
			SkippedRotten: skippedRotten,
			SkippedAPI:    skippedAPI,
			Failed:        failed,
		},
	}
}

func normalizeDepKey(path string) string {
	clean := filepath.Clean(path)
	clean = filepath.ToSlash(clean)
	if clean == "." {
		return clean
	}
	if !strings.HasPrefix(clean, ".") {
		clean = "./" + clean
	}
	return clean
}

func cloneDepStates(src map[string]bp.DepState) map[string]bp.DepState {
	clone := make(map[string]bp.DepState, len(src))
	for k, v := range src {
		clone[k] = v
	}
	return clone
}
