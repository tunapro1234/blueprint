package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	for _, target := range targets {
		bpObj, err := bp.LoadBlueprint(target)
		if err != nil {
			msg := normalizeYAMLError(err)
			line := fmt.Sprintf("✗ %s: %s", formatPath(target), msg)
			lines = append(lines, line)
			counts["stale"]++
			continue
		}
		statusLine, info, err := statusForBlueprint(bpObj)
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
			singleState = info.State
			singleChanged = info.ChangedFiles
			singleDeps = info.ChangedDeps
			singleReason = info.Reason
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
		data = StatusInfo{State: singleState, ChangedFiles: singleChanged, ChangedDeps: singleDeps, StaleReason: singleReason}
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

func statusForBlueprint(bpObj *bp.Blueprint) (string, bp.StalenessInfo, error) {
	info, err := bpObj.StalenessInfo()
	if err != nil {
		return "", info, err
	}
	switch info.State {
	case "no_snapshot":
		line := fmt.Sprintf("○ %s (no snapshot)", formatPath(bpObj.Dir))
		return line, info, nil
	case "fresh":
		line := fmt.Sprintf("✓ %s (fresh)", formatPath(bpObj.Dir))
		return line, info, nil
	case "stale":
		switch info.Reason {
		case "deps_changed", "deps_api_changed":
			deps := strings.Join(info.ChangedDeps, ", ")
			line := fmt.Sprintf("⚠ %s (deps changed: %s)", formatPath(bpObj.Dir), deps)
			return line, info, nil
		case "blueprint_changed":
			line := fmt.Sprintf("⚠ %s (stale, BLUEPRINT.yaml changed)", formatPath(bpObj.Dir))
			return line, info, nil
		default:
			count := len(info.ChangedFiles)
			if count == 0 {
				line := fmt.Sprintf("⚠ %s (stale)", formatPath(bpObj.Dir))
				return line, info, nil
			}
			line := fmt.Sprintf("⚠ %s (stale, %d files changed)", formatPath(bpObj.Dir), count)
			return line, info, nil
		}
	default:
		line := fmt.Sprintf("✓ %s (fresh)", formatPath(bpObj.Dir))
		return line, info, nil
	}
}
