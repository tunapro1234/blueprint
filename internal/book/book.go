package book

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	bptmux "blueprint/internal/tmux"
)

type Agent struct {
	Name     string `json:"name"`
	Folder   string `json:"folder,omitempty"`
	Parent   string `json:"parent,omitempty"`
	Class    string `json:"class,omitempty"`
	Role     string `json:"role,omitempty"`
	Status   string `json:"status,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	Color    string `json:"color,omitempty"`
}

type File struct {
	Updated      string  `json:"updated,omitempty"`
	Orchestrator string  `json:"orchestrator,omitempty"`
	Parent       string  `json:"parent,omitempty"`
	Agents       []Agent `json:"agents"`
}

func Paths(configured []string) []string {
	if path := os.Getenv("AGENTBOOK"); path != "" {
		return []string{path}
	}
	return append([]string(nil), configured...)
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
	loaded := 0
	for _, path := range paths {
		file, err := Load(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Fleet{}, err
		}
		if loaded == 0 && file.Orchestrator != "" {
			fleet.Root = file.Orchestrator
		}
		defaultParent := file.Parent
		if defaultParent == "" && loaded > 0 {
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
		loaded++
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
	if next.Color != "" {
		old.Color = next.Color
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
		f.Agents[name] = Agent{Name: name, Status: "unregistered"}
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

// IsDescendant reports whether name is below ancestor in the agentbook parent chain.
func (f Fleet) IsDescendant(name, ancestor string) bool {
	seen := map[string]bool{}
	for name != "" && !seen[name] {
		seen[name] = true
		name = f.Parents[name]
		if name == ancestor {
			return true
		}
	}
	return false
}

// SetColor records an agent's accent colour. Unlike SetStatus it never creates
// an entry: a colour is decoration, not a reason to register an agent.
func SetColor(paths []string, name, colour string) error {
	return mutate(paths, name, func(agent map[string]any) bool {
		if current, _ := agent["color"].(string); current == colour {
			return false
		}
		agent["color"] = colour
		return true
	})
}

// mutate applies change to the named agent in whichever book holds it, under
// the same lock and atomic rewrite SetStatus uses. change reports whether it
// altered anything; when it did not, the file is left untouched so a concurrent
// hand edit cannot be lost to a no-op write.
func mutate(paths []string, name string, change func(map[string]any) bool) error {
	paths = Paths(paths)
	if len(paths) == 0 {
		return fmt.Errorf("agentbook is not configured")
	}
	target := ""
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
		if target != "" {
			break
		}
	}
	if target == "" {
		return nil
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
	changed := false
	for _, value := range agents {
		agent, ok := value.(map[string]any)
		if ok && agent["name"] == name {
			changed = change(agent)
			break
		}
	}
	if !changed {
		return nil
	}
	raw["updated"] = time.Now().Format("2006-01-02")
	return writeBook(target, raw, info.Mode().Perm())
}

func writeBook(target string, raw map[string]any, mode os.FileMode) error {
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
	if err = tmp.Chmod(mode); err != nil {
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

func SetStatus(paths []string, name, status, folder, parent string) error {
	paths = Paths(paths)
	if len(paths) == 0 {
		return fmt.Errorf("agentbook is not configured")
	}
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
			currentStatus, _ := agent["status"].(string)
			currentFolder, _ := agent["folder"].(string)
			if currentStatus == status && currentFolder == folder {
				return nil
			}
			agent["status"] = status
			found = true
			break
		}
	}
	if !found {
		agent := map[string]any{"name": name, "folder": folder, "class": "other", "role": "(new - add role)", "status": status}
		if parent != "" {
			agent["parent"] = parent
		}
		agents = append(agents, agent)
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
