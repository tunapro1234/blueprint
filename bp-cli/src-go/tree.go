package bp

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type BlueprintTree struct {
	Root string
}

func (t *BlueprintTree) Walk() ([]*Blueprint, error) {
	root := t.Root
	if root == "" {
		root = "."
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var bps []*Blueprint
	stateDirs := map[string]string{}
	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			parent := filepath.Dir(path)
			if stateDirRel, ok := stateDirs[parent]; ok {
				if isStateDirName(d.Name(), stateDirRel) {
					return filepath.SkipDir
				}
			} else if d.Name() == ".bp" {
				return filepath.SkipDir
			} else if isDepsDirName(d.Name()) {
				return filepath.SkipDir
			}
			files, err := FindBlueprintFiles(path)
			if err != nil {
				return err
			}
			if len(files) > 0 {
				bp, err := LoadBlueprint(files[0])
				if err != nil {
					return err
				}
				bps = append(bps, bp)
				stateDirs[path] = bp.StateDirRel
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return bps, nil
}

func (t *BlueprintTree) ResolveDeps(bp *Blueprint) ([]*Blueprint, []string, error) {
	warnings := []string{}
	deps := []*Blueprint{}
	seen := map[string]struct{}{}
	overrides, err := bp.resolveHasBlueprintOverrides()
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(bp.Dir)
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if isDepsDirName(name) {
			continue
		}
		if isStateDirName(name, bp.StateDirRel) {
			continue
		}
		rel := filepath.Clean(name)
		if hb, ok := overrides[rel]; ok && hb != nil && !*hb {
			continue
		}
		subdir := filepath.Join(bp.Dir, name)
		bpPath, err := FindBlueprintFile(subdir)
		if err != nil {
			if err == ErrNotBlueprint {
				continue
			}
			return nil, nil, err
		}
		subBp, err := LoadBlueprint(bpPath)
		if err != nil {
			return nil, nil, err
		}
		if _, ok := seen[subBp.Dir]; ok {
			continue
		}
		seen[subBp.Dir] = struct{}{}
		deps = append(deps, subBp)
	}
	explicit, err := bp.Dependencies()
	if err != nil {
		return nil, nil, err
	}
	for _, dep := range explicit {
		if filepath.IsAbs(dep) {
			return nil, nil, fmt.Errorf("absolute dependency paths are not allowed: %s", dep)
		}
		clean := filepath.Clean(dep)
		target := filepath.Join(bp.Dir, clean)
		if _, err := os.Stat(target); err != nil {
			warnings = append(warnings, fmt.Sprintf("dependency not found: %s", dep))
			continue
		}
		bpPath, err := FindBlueprintFile(target)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("dependency has no blueprint: %s", dep))
			continue
		}
		subBp, err := LoadBlueprint(bpPath)
		if err != nil {
			return nil, nil, err
		}
		if _, ok := seen[subBp.Dir]; ok {
			continue
		}
		seen[subBp.Dir] = struct{}{}
		deps = append(deps, subBp)
	}
	sort.Slice(deps, func(i, j int) bool {
		return strings.Compare(deps[i].Dir, deps[j].Dir) < 0
	})
	return deps, warnings, nil
}

func (t *BlueprintTree) TopologicalSort() ([]*Blueprint, error) {
	bps, err := t.Walk()
	if err != nil {
		return nil, err
	}
	if len(bps) == 0 {
		return nil, nil
	}
	index := map[string]*Blueprint{}
	for _, bp := range bps {
		index[bp.Dir] = bp
	}
	inDegree := map[string]int{}
	graph := map[string][]string{}
	for _, bp := range bps {
		inDegree[bp.Dir] = 0
		graph[bp.Dir] = []string{}
	}
	for _, bp := range bps {
		deps, _, err := t.ResolveDeps(bp)
		if err != nil {
			return nil, err
		}
		for _, dep := range deps {
			if _, ok := index[dep.Dir]; !ok {
				continue
			}
			graph[dep.Dir] = append(graph[dep.Dir], bp.Dir)
			inDegree[bp.Dir]++
		}
	}
	queue := []string{}
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)
	result := []*Blueprint{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		result = append(result, index[id])
		for _, next := range graph[id] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
		sort.Strings(queue)
	}
	if len(result) != len(bps) {
		return nil, fmt.Errorf("circular dependency detected")
	}
	return result, nil
}
