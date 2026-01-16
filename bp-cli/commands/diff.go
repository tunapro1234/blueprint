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
	leftID, rightID := resolveDiffIDs(bpObj, id1, id2)
	if leftID == "" {
		return CommandResult{ExitCode: 1, Output: "No snapshot history", Errors: []string{"No snapshot history"}}
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
		return CommandResult{ExitCode: 0, Output: "No changes", Data: result}
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

func resolveDiffIDs(bpObj *bp.Blueprint, id1, id2 string) (string, string) {
	if id1 == "" && id2 == "" {
		currentID, err := readCurrentSnapshotID(bpObj.StateDir)
		if err != nil {
			return "", ""
		}
		return currentID, "current"
	}
	if id1 != "" && id2 == "" {
		return normalizeID(id1), "current"
	}
	return normalizeID(id1), normalizeID(id2)
}

func normalizeID(id string) string {
	if id == "" {
		return "current"
	}
	if strings.EqualFold(id, "current") {
		return "current"
	}
	return id
}

func readBlueprintContent(bpObj *bp.Blueprint, id string) (string, error) {
	if id == "current" {
		data, err := os.ReadFile(bpObj.Path)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	if !isSnapshotID(id) {
		return "", fmt.Errorf("Snapshot #%s not found", id)
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
