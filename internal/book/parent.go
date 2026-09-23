package book

import "fmt"

// SetParent moves an agent under a new parent in the agentbook. The only other
// way to change a registered parent was editing agentbook.json by hand under the
// lock (#23). The parent must already be registered and must not sit below the
// agent itself, or the tree would loop.
func SetParent(paths []string, name, parent string) (string, error) {
	fleet, err := LoadFleet(Paths(paths))
	if err != nil {
		return "", err
	}
	if _, ok := fleet.Agents[name]; !ok {
		return "", fmt.Errorf("unknown agent: %s", name)
	}
	if name == fleet.Root {
		return "", fmt.Errorf("%s is the root of the tree and has no parent", name)
	}
	if _, ok := fleet.Agents[parent]; !ok {
		return "", fmt.Errorf("unknown parent: %s", parent)
	}
	for at := parent; at != ""; at = fleet.Parents[at] {
		if at == name {
			return "", fmt.Errorf("%s is below %s; reparenting would create a cycle", parent, name)
		}
		if at == fleet.Root {
			break
		}
	}
	previous := fleet.Parents[name]
	if previous == parent {
		return previous, nil
	}
	return previous, mutate(paths, name, func(agent map[string]any) bool {
		agent["parent"] = parent
		return true
	})
}
