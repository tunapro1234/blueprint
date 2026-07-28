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

// Rename renames an agent across every configured book: its own entry, the
// `parent` of any agent below it, and mentions of the old name in role text.
// Those three can live in different books (a child under /srv/probot is written
// to the probot book while its parent sits in the main one), so every book is
// visited rather than only the one holding the entry.
//
// It returns one human-readable line per change, so `bp rename` can show what it
// touched instead of asking the reader to trust it.
func Rename(paths []string, old, name string) ([]string, error) {
	var changes []string
	for _, path := range Paths(paths) {
		if _, err := os.Stat(path); err != nil {
			continue // not every machine has every book
		}
		applied, err := editBook(path, func(raw map[string]any) []string {
			return renameInBook(raw, old, name, path)
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return changes, err
		}
		changes = append(changes, applied...)
	}
	return changes, nil
}

// PreviewRename reports what Rename would change in an already-parsed book. The
// caller owns raw (it parsed its own copy), so mutating it here is harmless and
// keeps preview and apply on exactly the same code path — a preview that drifts
// from the real thing is worse than none.
func PreviewRename(raw map[string]any, old, name, path string) []string {
	return renameInBook(raw, old, name, path)
}

func renameInBook(raw map[string]any, old, name, path string) []string {
	var changes []string
	label := filepath.Base(path)
	if current, _ := raw["orchestrator"].(string); current == old {
		raw["orchestrator"] = name
		changes = append(changes, label+": orchestrator -> "+name)
	}
	if current, _ := raw["parent"].(string); current == old {
		raw["parent"] = name
		changes = append(changes, label+": parent -> "+name)
	}
	agents, _ := raw["agents"].([]any)
	for _, value := range agents {
		agent, ok := value.(map[string]any)
		if !ok {
			continue
		}
		who, _ := agent["name"].(string)
		if who == old {
			agent["name"] = name
			who = name
			changes = append(changes, label+": name "+old+" -> "+name)
		}
		if parent, _ := agent["parent"].(string); parent == old {
			agent["parent"] = name
			changes = append(changes, label+": "+who+".parent -> "+name)
		}
		// Role text is prose, so only whole-word mentions are rewritten; a
		// substring rule would corrupt a longer name that contains the old one.
		if role, _ := agent["role"].(string); role != "" {
			if replaced := replaceWord(role, old, name); replaced != role {
				agent["role"] = replaced
				changes = append(changes, label+": "+who+".role mentions updated")
			}
		}
	}
	return changes
}

// replaceWord swaps whole-word occurrences of old, treating the agent-name
// characters [A-Za-z0-9._-] as word constituents so "probot-outreach-gpt" is
// never mangled while renaming "probot-outreach".
func replaceWord(text, old, name string) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		if strings.HasPrefix(text[i:], old) && !nameChar(byteAt(text, i-1)) && !nameChar(byteAt(text, i+len(old))) {
			out.WriteString(name)
			i += len(old)
			continue
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}

func byteAt(text string, i int) byte {
	if i < 0 || i >= len(text) {
		return 0
	}
	return text[i]
}

func nameChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '.' || b == '_' || b == '-'
}

// editBook applies change to one book under an exclusive lock, rewriting it
// atomically only when change reports something. Returning no changes leaves the
// file untouched, so a concurrent hand edit cannot be lost to a no-op write.
func editBook(path string, change func(map[string]any) []string) ([]string, error) {
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err = json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	changes := change(raw)
	if len(changes) == 0 {
		return nil, nil
	}
	raw["updated"] = time.Now().Format("2006-01-02")
	return changes, writeBook(path, raw, info.Mode().Perm())
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
