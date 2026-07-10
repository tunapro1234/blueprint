package book

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	bptmux "blueprint/internal/tmux"
)

const (
	MainPath   = "/srv/server-main/agentbook.json"
	ProbotPath = "/srv/probot/.orchestration/agentbook.json"
)

type Agent struct {
	Name     string `json:"name"`
	Folder   string `json:"folder,omitempty"`
	Parent   string `json:"parent,omitempty"`
	Class    string `json:"class,omitempty"`
	Role     string `json:"role,omitempty"`
	Status   string `json:"status,omitempty"`
	Nickname string `json:"nickname,omitempty"`
}

type File struct {
	Updated      string  `json:"updated,omitempty"`
	Orchestrator string  `json:"orchestrator,omitempty"`
	Parent       string  `json:"parent,omitempty"`
	Agents       []Agent `json:"agents"`
}

func Paths() []string {
	if path := os.Getenv("AGENTBOOK"); path != "" {
		return []string{path}
	}
	return []string{MainPath, ProbotPath}
}

func Load(path string) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return File{}, fmt.Errorf("%s: %w", path, err)
	}
	return file, nil
}

type Fleet struct {
	Agents  map[string]Agent
	Order   []string
	Parents map[string]string
	Root    string
}

func LoadFleet(paths []string) (Fleet, error) {
	fleet := Fleet{Agents: map[string]Agent{}, Parents: map[string]string{}, Root: "server-main"}
	for index, path := range paths {
		file, err := Load(path)
		if err != nil {
			return Fleet{}, err
		}
		if index == 0 && file.Orchestrator != "" {
			fleet.Root = file.Orchestrator
		}
		defaultParent := file.Parent
		if defaultParent == "" && index > 0 {
			defaultParent = fleet.Root
		}
		for _, agent := range file.Agents {
			if invalidName(agent.Name) {
				continue
			}
			if existing, ok := fleet.Agents[agent.Name]; ok {
				agent = merge(existing, agent)
			} else {
				fleet.Order = append(fleet.Order, agent.Name)
			}
			fleet.Agents[agent.Name] = agent
			parent := agent.Parent
			if parent == "" {
				if agent.Name == fleet.Root {
					parent = ""
				} else if defaultParent != "" {
					parent = defaultParent
				} else {
					parent = fleet.Root
				}
			}
			fleet.Parents[agent.Name] = parent
		}
	}
	if _, ok := fleet.Agents[fleet.Root]; !ok {
		fleet.Agents[fleet.Root] = Agent{Name: fleet.Root}
		fleet.Order = append([]string{fleet.Root}, fleet.Order...)
	}
	fleet.Parents[fleet.Root] = ""
	return fleet, nil
}

func merge(old, next Agent) Agent {
	if next.Folder != "" {
		old.Folder = next.Folder
	}
	if next.Parent != "" {
		old.Parent = next.Parent
	}
	if next.Class != "" {
		old.Class = next.Class
	}
	if next.Role != "" {
		old.Role = next.Role
	}
	if next.Status != "" {
		old.Status = next.Status
	}
	if next.Nickname != "" {
		old.Nickname = next.Nickname
	}
	return old
}

func invalidName(name string) bool {
	return name == "" || strings.ContainsAny(name, "*?[") || strings.Contains(name, " ")
}

func (f *Fleet) AddLive(names []string) {
	known := append([]string(nil), f.Order...)
	for _, name := range names {
		if _, ok := f.Agents[name]; ok {
			continue
		}
		parent, longest := f.Root, 0
		for _, candidate := range known {
			a := f.Agents[candidate]
			for _, prefix := range []string{candidate, a.Nickname} {
				if prefix != "" && strings.HasPrefix(name, prefix+"-") && len(prefix) > longest {
					parent, longest = candidate, len(prefix)
				}
			}
		}
		f.Agents[name] = Agent{Name: name, Status: "kayitsiz"}
		f.Parents[name] = parent
		f.Order = append(f.Order, name)
	}
}

func (f Fleet) SortedNames() []string {
	names := make([]string, 0, len(f.Agents))
	for name := range f.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func SetStatus(name, status, folder string) error {
	paths := Paths()
	target := paths[0]
	for _, path := range paths {
		file, err := Load(path)
		if err != nil {
			continue
		}
		for _, agent := range file.Agents {
			if agent.Name == name {
				target = path
				break
			}
		}
	}
	lock, err := os.OpenFile(target+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	data, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err = json.Unmarshal(data, &raw); err != nil {
		return err
	}
	agents, _ := raw["agents"].([]any)
	found := false
	for _, value := range agents {
		agent, ok := value.(map[string]any)
		if ok && agent["name"] == name {
			agent["status"] = status
			found = true
			break
		}
	}
	if !found {
		agents = append(agents, map[string]any{"name": name, "folder": folder, "class": "other", "role": "(yeni - rol gir)", "status": status})
	}
	raw["agents"] = agents
	raw["updated"] = time.Now().Format("2006-01-02")
	encoded, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(target), ".agentbook-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(encoded); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

type State struct {
	Alive bool
	Busy  bool
}

func LiveStates(ctx context.Context, client *bptmux.Client, fleet *Fleet) (map[string]State, error) {
	sessions, err := client.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	fleet.AddLive(sessions)
	states := make(map[string]State, len(sessions))
	for _, name := range sessions {
		pane, captureErr := client.Capture(ctx, name)
		states[name] = State{Alive: true, Busy: captureErr == nil && bptmux.Busy(pane)}
	}
	return states, nil
}
