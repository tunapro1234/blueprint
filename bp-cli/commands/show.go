package commands

import (
	"errors"
	"fmt"

	bp "blueprint"
)

func ShowCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
	}
	id, _ := ctx.Args["id"].(string)
	bpObj, err := bp.LoadBlueprint(path)
	if err != nil {
		if errors.Is(err, bp.ErrNotBlueprint) {
			out := fmt.Sprintf("✗ %s: not a blueprint package", formatPath(path))
			return CommandResult{ExitCode: 1, Output: out, Errors: []string{"not a blueprint package"}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if id == "" {
		current, err := readCurrentSnapshotID(bpObj.StateDir)
		if err != nil {
			if errors.Is(err, bp.ErrNoSnapshot) {
				return CommandResult{ExitCode: 1, Output: "No snapshot history", Errors: []string{"No snapshot history"}}
			}
			if errors.Is(err, ErrInvalidCurrentID) {
				return CommandResult{ExitCode: 1, Output: "Invalid current snapshot ID", Errors: []string{"Invalid current snapshot ID"}}
			}
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		id = current
	}
	content, err := readBlueprintContent(bpObj, id)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	return CommandResult{ExitCode: 0, Output: content}
}
