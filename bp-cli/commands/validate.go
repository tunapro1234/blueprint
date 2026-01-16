package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bp "blueprint"
)

func ValidateCommand(ctx CommandContext) CommandResult {
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
	exitCode := 0
	for _, target := range targets {
		bpObj, err := bp.LoadBlueprint(target)
		if err != nil {
			msg := normalizeYAMLError(err)
			line := fmt.Sprintf("✗ %s: %s", formatPath(target), msg)
			lines = append(lines, line)
			exitCode = maxExit(exitCode, 1)
			continue
		}
		res := bpObj.Validate()
		if len(res.Errors) > 0 {
			line := fmt.Sprintf("✗ %s: %s", formatPath(bpObj.Path), strings.Join(res.Errors, "; "))
			lines = append(lines, line)
			exitCode = maxExit(exitCode, 1)
			continue
		}
		if len(res.Warnings) > 0 {
			line := fmt.Sprintf("⚠ %s: %s", formatPath(bpObj.Path), strings.Join(res.Warnings, "; "))
			lines = append(lines, line)
			exitCode = maxExit(exitCode, 2)
			continue
		}
		line := fmt.Sprintf("✓ %s", formatPath(bpObj.Path))
		lines = append(lines, line)
	}
	return CommandResult{ExitCode: exitCode, Output: strings.Join(lines, "\n")}
}

func maxExit(current, next int) int {
	if current == 1 {
		return 1
	}
	if next == 1 {
		return 1
	}
	if current == 2 {
		return 2
	}
	if next == 2 {
		return 2
	}
	return 0
}
