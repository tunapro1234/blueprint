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
	safe, _ := ctx.Args["safe"].(bool)
	all, _ := ctx.Args["all"].(bool)
	bpObj, err := bp.LoadBlueprint(path)
	if err != nil {
		if errors.Is(err, bp.ErrNotBlueprint) {
			out := fmt.Sprintf("✗ %s: not a blueprint package", formatPath(path))
			return CommandResult{ExitCode: 1, Output: out, Errors: []string{"not a blueprint package"}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
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
	lines := []string{}
	dirty := false

	for _, key := range targets {
		depState := state.Deps[key]
		depDir := filepath.Join(bpObj.Dir, filepath.FromSlash(key))
		depDir = filepath.Clean(depDir)
		bpPath, err := bp.FindBlueprintFile(depDir)
		if err != nil {
			msg := fmt.Sprintf("Dependency not found: %s", key)
			return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
		}
		depBp, err := bp.LoadBlueprint(bpPath)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		latestID, err := readCurrentSnapshotID(depBp.StateDir)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		current := depState.Pinned
		if current == "" {
			current = depState.Latest
		}
		if current == latestID {
			continue
		}
		latestAPI := ""
		meta, err := bp.LoadSnapshotMeta(depBp.StateDir, latestID)
		if err == nil && meta.APIHash != "" {
			latestAPI = meta.APIHash
		} else {
			latestAPI, err = depBp.APIHash()
			if err != nil {
				return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
			}
		}
		apiChanged := depState.APIHash != "" && latestAPI != "" && depState.APIHash != latestAPI
		if apiChanged && safe {
			skipped = append(skipped, key)
			lines = append(lines, fmt.Sprintf("Skipped %s (API changed)", key))
			continue
		}

		depState.Pinned = latestID
		depState.Latest = latestID
		depState.APIHash = latestAPI
		depState.LatestAPIHash = latestAPI
		depState.APIChanged = false
		state.Deps[key] = depState
		upgraded = append(upgraded, key)
		if apiChanged {
			apiChangedList = append(apiChangedList, key)
			lines = append(lines, fmt.Sprintf("Upgraded %s (API CHANGED!)", key))
			lines = append(lines, fmt.Sprintf("⚠ Review diff: bp diff %s %s %s", key, current, latestID))
		} else {
			lines = append(lines, fmt.Sprintf("Upgraded %s to %s", key, latestID))
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
		depStateObj.Dependents[rel] = bp.DepRef{Using: latestID}
		if err := depBp.SaveState(depStateObj); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		dirty = true
	}

	if dirty {
		if err := bpObj.SaveState(state); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
	}

	if len(upgraded) == 0 {
		if len(skipped) == 0 {
			return CommandResult{ExitCode: 0, Output: "No upgrades available", Data: UpgradeResult{}}
		}
		return CommandResult{ExitCode: 0, Output: strings.Join(lines, "\n"), Data: UpgradeResult{Upgraded: upgraded, APIChanged: apiChangedList, Skipped: skipped}}
	}

	return CommandResult{ExitCode: 0, Output: strings.Join(lines, "\n"), Data: UpgradeResult{Upgraded: upgraded, APIChanged: apiChangedList, Skipped: skipped}}
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
