package book

import (
	"encoding/json"
	"fmt"
	"os"
	"syscall"
)

// EnsureLocalCoordinator replaces only the installer-created, phantom "local"
// root. Existing real coordinators and unrelated fields remain user-owned.
func EnsureLocalCoordinator(paths []string, folder string) (string, error) {
	paths = Paths(paths)
	if len(paths) == 0 {
		return "", fmt.Errorf("agentbook is not configured")
	}
	path := paths[0]
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var raw map[string]any
	if err = json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	root, _ := raw["orchestrator"].(string)
	agents, _ := raw["agents"].([]any)
	for _, entry := range agents {
		if agent, ok := entry.(map[string]any); ok && agent["name"] == root {
			return root, nil
		}
	}
	if root != "local" && root != "" {
		return "", fmt.Errorf("configured coordinator %q has no agent record; inspect bp book before onboarding", root)
	}
	for _, entry := range agents {
		agent, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if agent["name"] == "main" {
			return "", fmt.Errorf("main already exists outside the coordinator role; inspect bp book")
		}
		if agent["parent"] == "local" {
			agent["parent"] = "main"
		}
	}
	raw["orchestrator"] = "main"
	raw["agents"] = append(agents, map[string]any{"name": "main", "folder": folder, "role": "machine coordinator", "status": "closed", "colorOverride": "160"})
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return "main", writeBook(path, raw, info.Mode().Perm())
}
