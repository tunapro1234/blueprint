package commands

import (
	"path/filepath"
	"sort"
	"strings"

	bp "blueprint"
)

func depUpgradesFromState(deps map[string]bp.DepState) []DepUpgrade {
	if len(deps) == 0 {
		return nil
	}
	keys := make([]string, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []DepUpgrade{}
	for _, key := range keys {
		dep := deps[key]
		pinned := dep.Pinned
		if pinned == "" {
			pinned = dep.Latest
		}
		latest := dep.Latest
		if pinned == "" || latest == "" {
			continue
		}
		if pinned == latest {
			continue
		}
		apiChanged := dep.APIChanged
		if !apiChanged && dep.APIHash != "" && dep.LatestAPIHash != "" && dep.APIHash != dep.LatestAPIHash {
			apiChanged = true
		}
		out = append(out, DepUpgrade{Path: key, Current: pinned, Latest: latest, APIChanged: apiChanged})
	}
	return out
}

func formatDepUpgradeWarnings(deps map[string]bp.DepState) []string {
	updates := depUpgradesFromState(deps)
	lines := []string{}
	for _, up := range updates {
		line := formatDepUpgradeLine(up)
		lines = append(lines, strings.TrimPrefix(line, "⚠ "))
	}
	return lines
}

func apiHashForSnapshot(dep *bp.Blueprint, id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", nil
	}
	meta, err := bp.LoadSnapshotMeta(dep.StateDir, id)
	if err == nil && meta.APIHash != "" {
		return meta.APIHash, nil
	}
	return dep.APIHash()
}

func relativeLabel(fromDir, toDir string) (string, error) {
	rel, err := filepath.Rel(fromDir, toDir)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return ".", nil
	}
	if !strings.HasPrefix(rel, ".") {
		rel = "./" + rel
	}
	return rel, nil
}
