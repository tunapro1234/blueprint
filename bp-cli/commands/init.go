package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	bp "blueprint"
)

const blueprintTemplate = `# {folder_name}

_meta:
  version: "0.1.0"
  updated: {date}
  status: draft

intent: TODO

overview: |
  TODO

api:
  exports: []

implementation:
  structure: []

tests:
  scenarios: []
`

func InitCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return CommandResult{ExitCode: 1, Output: "path not found", Errors: []string{"path not found"}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if !info.IsDir() {
		return CommandResult{ExitCode: 1, Output: "path is not a directory", Errors: []string{"path is not a directory"}}
	}
	if _, err := bp.FindBlueprintFile(path); err == nil {
		return CommandResult{ExitCode: 1, Output: "BLUEPRINT.yaml already exists", Errors: []string{"BLUEPRINT.yaml already exists"}}
	} else if !errors.Is(err, bp.ErrNotBlueprint) {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	folderName := filepath.Base(filepath.Clean(path))
	date := time.Now().Format("2006-01-02")
	content := strings.ReplaceAll(blueprintTemplate, "{folder_name}", folderName)
	content = strings.ReplaceAll(content, "{date}", date)
	filePath := filepath.Join(path, "BLUEPRINT.yaml")
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	return CommandResult{ExitCode: 0, Output: fmt.Sprintf("Created %s", formatPath(filePath))}
}
