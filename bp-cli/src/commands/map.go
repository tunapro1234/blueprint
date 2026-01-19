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

func MapCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
	}
	noRecursive, _ := ctx.Args["no_recursive"].(bool)
	var targets []string
	if !noRecursive {
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
	exitCode := 0
	for idx, target := range targets {
		bpObj, err := bp.LoadBlueprint(target)
		if err != nil {
			msg := normalizeYAMLError(err)
			line := fmt.Sprintf("✗ %s: %s", formatPath(target), msg)
			lines = append(lines, line)
			exitCode = 1
			continue
		}
		warnRottenDependencies(bpObj)

		label := formatPath(bpObj.Dir)
		if !strings.HasSuffix(label, "/") {
			label += "/"
		}
		lines = append(lines, label)

		state, err := bp.LoadState(filepath.Join(bpObj.StateDir, "state.yaml"))
		if err != nil && !errors.Is(err, bp.ErrNoSnapshot) {
			lines = append(lines, fmt.Sprintf("  snapshot: (error: %s)", err.Error()))
			exitCode = 1
			continue
		}

		snapshotID := "(none)"
		apiHash := "(none)"
		rottenSnapshot := false
		if state != nil && state.SnapshotID != "" {
			snapshotID = state.SnapshotID
			if meta, err := bp.LoadSnapshotMeta(bpObj.StateDir, snapshotID); err == nil {
				if meta.APIHash != "" {
					apiHash = meta.APIHash
				}
				rottenSnapshot = meta.Rotten
			}
		}
		if apiHash == "(none)" {
			if hash, err := bpObj.APIHash(); err == nil && hash != "" {
				apiHash = hash
			}
		}
		if rottenSnapshot {
			lines = append(lines, fmt.Sprintf("  snapshot: %s [ROTTEN]", snapshotID))
		} else {
			lines = append(lines, fmt.Sprintf("  snapshot: %s", snapshotID))
		}
		lines = append(lines, fmt.Sprintf("  api: %s", apiHash))

		depsState, _, err := bpObj.DependencyState()
		if err != nil {
			lines = append(lines, fmt.Sprintf("  deps: (error: %s)", err.Error()))
			exitCode = 1
		} else if len(depsState) == 0 {
			lines = append(lines, "  deps: (none)")
		} else {
			lines = append(lines, "  deps:")
			keys := make([]string, 0, len(depsState))
			for k := range depsState {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, key := range keys {
				dep := depsState[key]
				id := dep.Pinned
				if id == "" {
					id = dep.Latest
				}
				api := dep.APIHash
				if api == "" {
					api = dep.LatestAPIHash
				}
				line := fmt.Sprintf("    %s @ %s (api: %s)", key, id, api)
				if dep.Rotten {
					line += " [ROTTEN]"
				}
				lines = append(lines, line)
			}
		}
		if idx < len(targets)-1 {
			lines = append(lines, "")
		}
	}

	return CommandResult{ExitCode: exitCode, Output: strings.Join(lines, "\n")}
}
