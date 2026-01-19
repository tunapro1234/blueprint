package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	bp "blueprint"
	"github.com/pmezard/go-difflib/difflib"
)

func DiffCommand(ctx CommandContext) CommandResult {
	path := ctx.Path
	if path == "" {
		path = "."
	}
	id1, _ := ctx.Args["id1"].(string)
	id2, _ := ctx.Args["id2"].(string)
	bpObj, err := bp.LoadBlueprint(path)
	if err != nil {
		if errors.Is(err, bp.ErrNotBlueprint) {
			out := fmt.Sprintf("✗ %s: not a blueprint package", formatPath(path))
			return CommandResult{ExitCode: 1, Output: out, Errors: []string{"not a blueprint package"}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	warnRottenDependencies(bpObj)
	leftID, rightID, err := resolveDiffIDs(bpObj, id1, id2)
	if err != nil {
		if errors.Is(err, bp.ErrNoSnapshot) {
			return CommandResult{ExitCode: 1, Output: "No snapshot history", Errors: []string{"No snapshot history"}}
		}
		if errors.Is(err, ErrInvalidCurrentID) {
			return CommandResult{ExitCode: 1, Output: "Invalid current snapshot ID", Errors: []string{"Invalid current snapshot ID"}}
		}
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	leftContent, err := readBlueprintContent(bpObj, leftID)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	rightContent, err := readBlueprintContent(bpObj, rightID)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	result := DiffResult{LeftID: leftID, RightID: rightID}
	if leftContent == rightContent {
		result.HasChanges = false
		result.UnifiedDiff = ""
		return CommandResult{ExitCode: 0, Output: "", Data: result}
	}
	diff := difflib.UnifiedDiff{
		A:        splitLines(leftContent),
		B:        splitLines(rightContent),
		FromFile: labelForID(leftID),
		ToFile:   labelForID(rightID),
		Context:  3,
	}
	text, err := difflib.GetUnifiedDiffString(diff)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	result.HasChanges = true
	result.UnifiedDiff = text
	return CommandResult{ExitCode: 0, Output: text, Data: result}
}

func resolveDiffIDs(bpObj *bp.Blueprint, id1, id2 string) (string, string, error) {
	if id1 == "" && id2 == "" {
		currentID, err := readCurrentSnapshotID(bpObj.StateDir)
		if err != nil {
			return "", "", err
		}
		return currentID, "current", nil
	}
	if id1 != "" && id2 == "" {
		leftID, err := resolveSnapshotID(bpObj.StateDir, id1)
		if err != nil {
			return "", "", err
		}
		return leftID, "current", nil
	}
	leftID, err := resolveSnapshotID(bpObj.StateDir, id1)
	if err != nil {
		return "", "", err
	}
	rightID, err := resolveSnapshotID(bpObj.StateDir, id2)
	if err != nil {
		return "", "", err
	}
	return leftID, rightID, nil
}

func readBlueprintContent(bpObj *bp.Blueprint, id string) (string, error) {
	if id == "current" {
		data, err := os.ReadFile(bpObj.Path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	path := snapshotBlueprintPath(bpObj.StateDir, id)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("Snapshot #%s not found", id)
		}
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func splitLines(text string) []string {
	if text == "" {
		return []string{}
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	return lines
}

func labelForID(id string) string {
	if id == "current" {
		return "current"
	}
	return "snapshot #" + id
}
