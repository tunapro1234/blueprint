package commands

import (
	"errors"
	"fmt"

	bp "blueprint"
)

func CancelCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
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
	active, err := hasImplLock(bpObj.StateDir)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if !active {
		out := "No active implementation."
		return CommandResult{ExitCode: 1, Output: out, Errors: []string{out}}
	}
	if err := clearImplLock(bpObj.StateDir); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	return CommandResult{ExitCode: 0, Output: "Implementation cancelled."}
}
