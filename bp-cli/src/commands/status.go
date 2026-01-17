package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	bp "blueprint"
)

func StatusCommand(ctx CommandContext) CommandResult {
	recursive, _ := ctx.Args["recursive"].(bool)
	path := ctx.Path
	if path == "" {
		path = "."
	}
	var targets []string
	if recursive {
		files, err := findBlueprintsRecursive(path)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		targets = files
	} else {
		info, err := os.Stat(path)
		if err != nil {
			msg := err.Error()
			if os.IsNotExist(err) {
				msg = "file not found"
			}
			out := fmt.Sprintf("✗ %s: %s", formatPath(path), msg)
			return CommandResult{ExitCode: 1, Output: out, Errors: []string{msg}}
		}
		if info.IsDir() {
			bpPath, err := bp.FindBlueprintFile(path)
			if err != nil {
				if errors.Is(err, bp.ErrNotBlueprint) {
					target := filepath.Join(path, "BLUEPRINT.yaml")
					out := fmt.Sprintf("✗ %s: file not found", formatPath(target))
					return CommandResult{ExitCode: 1, Output: out, Errors: []string{"file not found"}}
				}
				return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
			}
			targets = []string{bpPath}
		} else {
			targets = []string{path}
		}
	}
	if len(targets) == 0 {
		out := fmt.Sprintf("✗ %s: file not found", formatPath(filepath.Join(path, "BLUEPRINT.yaml")))
		return CommandResult{ExitCode: 1, Output: out, Errors: []string{"file not found"}}
	}
	lines := []string{}
	counts := map[string]int{"fresh": 0, "stale": 0, "no_snapshot": 0}
	var singleState string
	var singleChanged []string
	var singleDeps []string
	var singleReason string
	var singleUpdates []DepUpgrade
	var singleDependents []bp.DepRef
	for _, target := range targets {
		bpObj, err := bp.LoadBlueprint(target)
		if err != nil {
			msg := normalizeYAMLError(err)
			line := fmt.Sprintf("✗ %s: %s", formatPath(target), msg)
			lines = append(lines, line)
			counts["stale"]++
			continue
		}
		statusLine, info, upgrades, dependentsLines, dependentsData, err := statusForBlueprint(bpObj)
		if err != nil {
			msg := err.Error()
			line := fmt.Sprintf("✗ %s: %s", formatPath(bpObj.Path), msg)
			lines = append(lines, line)
			counts["stale"]++
			continue
		}
		counts[info.State]++
		lines = append(lines, statusLine)
		if !recursive {
			for _, up := range upgrades {
				lines = append(lines, formatDepUpgradeLine(up))
			}
			if len(dependentsLines) > 0 {
				lines = append(lines, dependentsLines...)
			}
			singleState = info.State
			singleChanged = info.ChangedFiles
			singleDeps = info.ChangedDeps
			singleReason = info.Reason
			singleUpdates = upgrades
			singleDependents = dependentsData
		}
	}
	if recursive {
		lines = append(lines, "---")
		summary := []string{
			fmt.Sprintf("%d fresh", counts["fresh"]),
			fmt.Sprintf("%d stale", counts["stale"]),
		}
		if counts["no_snapshot"] > 0 {
			summary = append(summary, fmt.Sprintf("%d no snapshot", counts["no_snapshot"]))
		}
		lines = append(lines, strings.Join(summary, ", "))
	}
	exitCode := 0
	if counts["no_snapshot"] > 0 {
		exitCode = 2
	} else if counts["stale"] > 0 {
		exitCode = 1
	}
	data := any(nil)
	if !recursive {
		data = StatusInfo{
			State:        singleState,
			ChangedFiles: singleChanged,
			ChangedDeps:  singleDeps,
			StaleReason:  singleReason,
			DepUpdates:   singleUpdates,
			Dependents:   singleDependents,
		}
	}
	return CommandResult{ExitCode: exitCode, Output: strings.Join(lines, "\n"), Data: data}
}

func statusState(counts map[string]int) string {
	if counts["no_snapshot"] > 0 {
		return "no_snapshot"
	}
	if counts["stale"] > 0 {
		return "stale"
	}
	return "fresh"
}

func statusForBlueprint(bpObj *bp.Blueprint) (string, bp.StalenessInfo, []DepUpgrade, []string, []bp.DepRef, error) {
	info, err := bpObj.StalenessInfo()
	if err != nil {
		return "", info, nil, nil, nil, err
	}
	line := ""
	switch info.State {
	case "no_snapshot":
		line = fmt.Sprintf("○ %s (no snapshot)", formatPath(bpObj.Dir))
	case "fresh":
		line = fmt.Sprintf("✓ %s (fresh)", formatPath(bpObj.Dir))
	case "stale":
		switch info.Reason {
		case "deps_changed", "deps_api_changed":
			deps := strings.Join(info.ChangedDeps, ", ")
			line = fmt.Sprintf("⚠ %s (deps changed: %s)", formatPath(bpObj.Dir), deps)
		case "blueprint_changed":
			line = fmt.Sprintf("⚠ %s (stale, BLUEPRINT.yaml changed)", formatPath(bpObj.Dir))
		default:
			count := len(info.ChangedFiles)
			if count == 0 {
				line = fmt.Sprintf("⚠ %s (stale)", formatPath(bpObj.Dir))
			} else {
				line = fmt.Sprintf("⚠ %s (stale, %d files changed)", formatPath(bpObj.Dir), count)
			}
		}
	default:
		line = fmt.Sprintf("✓ %s (fresh)", formatPath(bpObj.Dir))
	}

	upgrades := []DepUpgrade{}
	if info.State != "no_snapshot" {
		if depState, _, err := bpObj.DependencyState(); err == nil {
			upgrades = depUpgradesFromState(depState)
		}
	}

	dependentsLines, dependentsData := buildDependentsOutput(bpObj)
	return line, info, upgrades, dependentsLines, dependentsData, nil
}

func formatDepUpgradeLine(up DepUpgrade) string {
	line := fmt.Sprintf("⚠ %s: %s → %s", up.Path, up.Current, up.Latest)
	if up.APIChanged {
		line += " (API CHANGED!)"
	}
	return line
}

func buildDependentsOutput(bpObj *bp.Blueprint) ([]string, []bp.DepRef) {
	state, err := bp.LoadState(filepath.Join(bpObj.StateDir, "state.yaml"))
	if err != nil {
		return nil, nil
	}
	if len(state.Dependents) == 0 {
		return nil, nil
	}
	currentID, err := readCurrentSnapshotID(bpObj.StateDir)
	if err != nil {
		return nil, nil
	}
	history, _ := bpObj.GetHistory()
	index := map[string]int{}
	for i, entry := range history {
		index[entry.ID] = i
	}
	keys := make([]string, 0, len(state.Dependents))
	for k := range state.Dependents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := []string{"Dependents:"}
	refs := []bp.DepRef{}
	for _, key := range keys {
		ref := state.Dependents[key]
		suffix := ""
		if ref.Using == currentID {
			suffix = " (current)"
		} else {
			behind := 0
			idxUsing, okUsing := index[ref.Using]
			idxCurrent, okCurrent := index[currentID]
			if okUsing && okCurrent && idxUsing > idxCurrent {
				behind = idxUsing - idxCurrent
			}
			if behind > 0 {
				suffix = fmt.Sprintf(" (OUTDATED - %d behind)", behind)
			} else {
				suffix = " (OUTDATED)"
			}
		}
		lines = append(lines, fmt.Sprintf("  %s using %s%s", key, ref.Using, suffix))
		refs = append(refs, ref)
	}
	return lines, refs
}
