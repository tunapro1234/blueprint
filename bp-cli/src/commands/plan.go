package commands

import (
	"errors"
	"fmt"
	"strings"

	bp "blueprint"
)

func PlanCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
	}
	noRecursive, _ := ctx.Args["no_recursive"].(bool)
	if !noRecursive {
		return planRecursive(path)
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
	lines, steps, err := planForBlueprints([]*bp.Blueprint{bpObj})
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if len(lines) == 0 {
		return CommandResult{ExitCode: 0, Output: "All blueprints fresh", Data: PlanResult{Steps: steps}}
	}
	return CommandResult{ExitCode: 0, Output: strings.Join(lines, "\n"), Data: PlanResult{Steps: steps}}
}

func planRecursive(root string) CommandResult {
	tree := &bp.BlueprintTree{Root: root}
	ordered, err := tree.TopologicalSort()
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	lines, steps, err := planForBlueprints(ordered)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if len(lines) == 0 {
		return CommandResult{ExitCode: 0, Output: "All blueprints fresh", Data: PlanResult{Steps: steps}}
	}
	return CommandResult{ExitCode: 0, Output: strings.Join(lines, "\n"), Data: PlanResult{Steps: steps}}
}

func planForBlueprints(bps []*bp.Blueprint) ([]string, []PlanStep, error) {
	lines := []string{}
	steps := []PlanStep{}
	idx := 1
	for _, bpObj := range bps {
		warnRottenDependencies(bpObj)
		info, err := bpObj.StalenessInfo()
		if err != nil {
			return nil, nil, err
		}
		if info.State == "fresh" {
			continue
		}
		label := formatPath(bpObj.Dir)
		desc := formatPlanDesc(info.State, info.Reason)
		lines = append(lines, fmt.Sprintf("%d. %s (%s)", idx, label, desc))
		steps = append(steps, PlanStep{Path: label, State: info.State, Reason: info.Reason})
		idx++
	}
	return lines, steps, nil
}

func formatPlanDesc(state, reason string) string {
	if state == "no_snapshot" {
		return "no snapshot"
	}
	if reason == "" {
		return state
	}
	return fmt.Sprintf("%s, %s", state, reason)
}
