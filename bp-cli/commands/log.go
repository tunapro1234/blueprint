package commands

import (
	"errors"
	"fmt"
	"strings"

	bp "blueprint"
)

func LogCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
	}
	count := 10
	if raw, ok := ctx.Args["count"].(int); ok && raw > 0 {
		count = raw
	}
	bpObj, err := bp.LoadBlueprint(path)
	if err != nil {
		if errors.Is(err, bp.ErrNotBlueprint) {
			out := fmt.Sprintf("✗ %s: not a blueprint package", formatPath(path))
			return CommandResult{ExitCode: 1, Output: out, Errors: []string{"not a blueprint package"}}
		}
		out := fmt.Sprintf("✗ %s: %s", formatPath(path), err.Error())
		return CommandResult{ExitCode: 1, Output: out, Errors: []string{err.Error()}}
	}
	history, err := bpObj.GetHistory()
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if len(history) == 0 {
		return CommandResult{ExitCode: 0, Output: "No snapshot history", Data: history}
	}
	if count > len(history) {
		count = len(history)
	}
	lines := []string{}
	for i := 0; i < count; i++ {
		entry := history[i]
		ts := formatTimestamp(entry.Timestamp)
		line := fmt.Sprintf("#%s %s \"%s\"", entry.ID, ts, entry.Message)
		lines = append(lines, line)
	}
	return CommandResult{ExitCode: 0, Output: strings.Join(lines, "\n"), Data: history}
}
