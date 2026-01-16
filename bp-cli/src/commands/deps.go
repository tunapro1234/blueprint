package commands

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	bp "blueprint"
)

func DepsCommand(ctx CommandContext) CommandResult {
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
		out := fmt.Sprintf("✗ %s: %s", formatPath(path), err.Error())
		return CommandResult{ExitCode: 1, Output: out, Errors: []string{err.Error()}}
	}
	root := bpObj.Dir
	tree := &bp.BlueprintTree{Root: root}
	ordered, err := tree.TopologicalSort()
	if err != nil {
		msg := cycleMessage(tree, root)
		if msg == "" {
			msg = "Circular dependency detected"
		}
		return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
	}
	lines := []string{}
	for i, item := range ordered {
		label := formatDepLabel(root, item.Dir)
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, label))
	}
	return CommandResult{ExitCode: 0, Output: strings.Join(lines, "\n"), Data: pathsFromBlueprints(ordered, root)}
}

func formatDepLabel(root, dir string) string {
	if samePath(root, dir) {
		return ". (current)"
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return formatPath(dir)
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, ".") {
		rel = "./" + rel
	}
	return rel
}

func samePath(a, b string) bool {
	cleanA := filepath.Clean(a)
	cleanB := filepath.Clean(b)
	return cleanA == cleanB
}

func pathsFromBlueprints(list []*bp.Blueprint, root string) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, formatDepLabel(root, item.Dir))
	}
	return out
}

func cycleMessage(tree *bp.BlueprintTree, root string) string {
	graph, err := buildDepGraph(tree)
	if err != nil {
		return ""
	}
	cycle := findCycle(graph)
	if len(cycle) == 0 {
		return ""
	}
	labels := make([]string, 0, len(cycle))
	for _, dir := range cycle {
		labels = append(labels, formatDepLabel(root, dir))
	}
	return fmt.Sprintf("Circular dependency detected: %s", strings.Join(labels, " → "))
}

func buildDepGraph(tree *bp.BlueprintTree) (map[string][]string, error) {
	bps, err := tree.Walk()
	if err != nil {
		return nil, err
	}
	graph := map[string][]string{}
	for _, item := range bps {
		deps, _, err := tree.ResolveDeps(item)
		if err != nil {
			return nil, err
		}
		list := []string{}
		for _, dep := range deps {
			list = append(list, dep.Dir)
		}
		graph[item.Dir] = list
	}
	return graph, nil
}

func findCycle(graph map[string][]string) []string {
	visited := map[string]bool{}
	stack := map[string]bool{}
	parent := map[string]string{}
	var cycle []string
	var dfs func(node string) bool
	dfs = func(node string) bool {
		visited[node] = true
		stack[node] = true
		for _, next := range graph[node] {
			if !visited[next] {
				parent[next] = node
				if dfs(next) {
					return true
				}
			} else if stack[next] {
				cycle = buildCyclePath(next, node, parent)
				return true
			}
		}
		stack[node] = false
		return false
	}
	for node := range graph {
		if !visited[node] {
			if dfs(node) {
				break
			}
		}
	}
	return cycle
}

func buildCyclePath(start, end string, parent map[string]string) []string {
	path := []string{start}
	curr := end
	for curr != start && curr != "" {
		path = append(path, curr)
		curr = parent[curr]
	}
	path = append(path, start)
	reverse(path)
	return path
}

func reverse(items []string) {
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
}
