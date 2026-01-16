package bp

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func IsBlueprintFile(name string) bool {
	lower := strings.ToLower(name)
	if lower == "blueprint.yaml" || lower == ".blueprint.yaml" {
		return true
	}
	if strings.HasPrefix(lower, "blueprint.") && strings.HasSuffix(lower, ".yaml") {
		return true
	}
	if strings.HasPrefix(lower, ".blueprint.") && strings.HasSuffix(lower, ".yaml") {
		return true
	}
	if strings.HasSuffix(lower, ".bp.yaml") {
		return true
	}
	return false
}

func FindBlueprintFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !IsBlueprintFile(name) {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Slice(files, func(i, j int) bool {
		pi := blueprintPriority(filepath.Base(files[i]))
		pj := blueprintPriority(filepath.Base(files[j]))
		if pi != pj {
			return pi < pj
		}
		return files[i] < files[j]
	})
	return files, nil
}

func blueprintPriority(name string) int {
	lower := strings.ToLower(name)
	switch {
	case lower == "blueprint.yaml":
		return 0
	case lower == ".blueprint.yaml":
		return 1
	case strings.HasPrefix(lower, "blueprint.") && strings.HasSuffix(lower, ".yaml"):
		return 2
	case strings.HasPrefix(lower, ".blueprint.") && strings.HasSuffix(lower, ".yaml"):
		return 3
	case strings.HasSuffix(lower, ".bp.yaml"):
		return 4
	default:
		return 5
	}
}

func FindBlueprintFile(dir string) (string, error) {
	files, err := FindBlueprintFiles(dir)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", ErrNotBlueprint
	}
	return files[0], nil
}

func relPath(base, path string) (string, error) {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func isUnderBlueprintState(rel string) bool {
	if rel == ".blueprint" {
		return true
	}
	if strings.HasPrefix(rel, ".blueprint/") {
		return true
	}
	return false
}
