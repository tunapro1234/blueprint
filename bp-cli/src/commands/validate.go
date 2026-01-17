package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bp "blueprint"
)

const (
	apiLineLimit  = 300
	implLineLimit = 900
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

		if depState, _, err := bpObj.DependencyState(); err == nil {
			for _, up := range depUpgradesFromState(depState) {
				lines = append(lines, formatDepUpgradeLine(up))
			}
		}

		res := bpObj.Validate()
		if len(res.Errors) > 0 {
			line := fmt.Sprintf("✗ %s: %s", formatPath(bpObj.Path), strings.Join(res.Errors, "; "))
			lines = append(lines, line)
			exitCode = maxExit(exitCode, 1)
			continue
		}
		lineWarnings, err := lineLimitWarnings(bpObj.Path)
		if err != nil {
			lines = append(lines, fmt.Sprintf("⚠ %s: %s", formatPath(bpObj.Path), err.Error()))
			exitCode = maxExit(exitCode, 2)
			continue
		}
		warnings := append(res.Warnings, lineWarnings...)
		if len(warnings) > 0 {
			line := fmt.Sprintf("⚠ %s: %s", formatPath(bpObj.Path), strings.Join(warnings, "; "))
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

func lineLimitWarnings(path string) ([]string, error) {
	warnings := []string{}
	if count, ok, err := sectionLineCount(path, "api"); err != nil {
		return warnings, err
	} else if ok && count > apiLineLimit {
		warnings = append(warnings, fmt.Sprintf("api exceeds %d lines (%d lines)", apiLineLimit, count))
	}
	if count, ok, err := sectionLineCount(path, "implementation"); err != nil {
		return warnings, err
	} else if ok && count > implLineLimit {
		warnings = append(warnings, fmt.Sprintf("implementation exceeds %d lines (%d lines)", implLineLimit, count))
	}
	return warnings, nil
}

func sectionLineCount(path, section string) (int, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	baseIndent := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, section+":") && indentLevel(line) == 0 {
			start = i
			baseIndent = indentLevel(line)
			break
		}
	}
	if start == -1 {
		return 0, false, nil
	}
	count := 0
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			count++
			continue
		}
		indent := indentLevel(line)
		if indent <= baseIndent && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			break
		}
		count++
	}
	return count, true, nil
}

func indentLevel(line string) int {
	count := 0
	for _, r := range line {
		if r == ' ' {
			count++
			continue
		}
		if r == '\t' {
			count += 2
			continue
		}
		break
	}
	return count
}
