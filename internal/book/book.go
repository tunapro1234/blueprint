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
	known := make([]Agent, 0, len(f.Order))
	for _, name := range f.Order {
		known = append(known, f.Agents[name])
	}
	for _, name := range names {
		if _, ok := f.Agents[name]; ok {
			continue
		}
		parent, class := f.Root, ""
		if index := infer(known, name, ""); index >= 0 {
			candidate := known[index]
			parent, class = candidate.Name, candidate.Class
		}
		f.Agents[name] = Agent{Name: name, Class: class, Status: "unregistered"}
		f.Parents[name] = parent
		f.Order = append(f.Order, name)
	}
}

// FirstPath strips the annotation an agentbook folder may carry, e.g.
// "/srv (home: /srv/server-main)".
func FirstPath(folder string) string {
	fields := strings.Fields(folder)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "/") {
		return fields[0]
	}
	return ""
}

// under reports whether folder sits at or below root, comparing whole path
// components so /srv/kavram-old is not read as living under /srv/kavram. Both
// arguments must already be cleaned; "." means "no path".
func under(folder, root string) bool {
	if folder == "." || root == "." {
		return false
	}
	return folder == root ||
		root == string(filepath.Separator) ||
		strings.HasPrefix(folder, root+string(filepath.Separator))
}

// FolderHint returns the one line `bp open` prints when an agent's folder does
// not sit under its parent's. The fleet wants the hierarchy visible on disk,
// but the rule is advice, never a refusal: the caller prints this and carries
// on. An empty result means "say nothing", which covers every case where the
// layout is either unknown or legitimately flat:
//   - no parent, or the parent is the fleet root (everything is below it);
//   - either folder unknown;
//   - the same folder as the parent — worker agents share their parent's repo;
//   - a git worktree path, which lives under whichever repo it was cut from;
//   - a folder already at or under the parent's.
func FolderHint(name, folder, parent, parentFolder, root string) string {
	if parent == "" || parent == root {
		return ""
	}
	child := filepath.Clean(FirstPath(folder))
	above := filepath.Clean(FirstPath(parentFolder))
	if child == "." || above == "." || child == above || isWorktree(child) || under(child, above) {
		return ""
	}
	return fmt.Sprintf("oneri: %s klasoru ebeveyni %s altinda degil (%s vs %s) — hiyerarsi klasor yapisinda da gorunsun",
		name, parent, child, above)
}

// isWorktree reports whether path holds a ".worktrees" component, the layout
// `bp worktree` creates at <repo>/.worktrees/<topic>.
func isWorktree(path string) bool {
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".worktrees" {
			return true
		}
	}
	return false
}

func infer(agents []Agent, name, folder string) int {
	folder = filepath.Clean(FirstPath(folder))
	best, longest := -1, 0
	for i, agent := range agents {
		candidate := filepath.Clean(FirstPath(agent.Folder))
		if under(folder, candidate) && len(candidate) > longest {
			best, longest = i, len(candidate)
		}
	}
	if best >= 0 {
		return best
	}
	longest = 0
	for i, agent := range agents {
		for _, prefix := range []string{agent.Name, agent.Nickname} {
			if prefix != "" && strings.HasPrefix(name, prefix+"-") && len(prefix) > longest {
				best, longest = i, len(prefix)
			}
		}
	}
	return best
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

// Registration carries what a caller knows about an agent it is registering.
// Sender is only a fallback parent; Parent and Role are explicit pins (bp open
// --parent / --role) and, when set, beat anything inference would produce.
type Registration struct {
	Sender string
	Parent string
	Role   string
}

// SetStatus records an agent's status in whichever book holds it, creating the
// entry when no book does. The status is stored as written and never validated
// here: readers display whatever they find and only ever compare it, so the
// vocabulary can grow without a migration. Today it is "open", "closed" and
// "opening" — the last one meaning a `bp open` that started but has not been
// confirmed, which is why an unknown status must stay harmless to old readers
// rather than be rejected by new ones.
func SetStatus(paths []string, name, status, folder string, reg Registration) error {
	paths = Paths(paths)
	if len(paths) == 0 {
		return fmt.Errorf("agentbook is not configured")
	}
	target := paths[0]
	parent := reg.Sender
	var candidates []Agent
	var owners []string
	foundName := false
	for _, path := range paths {
		file, err := Load(path)
		if err != nil {
			continue
		}
		for _, agent := range file.Agents {
			candidates = append(candidates, agent)
			owners = append(owners, path)
			if agent.Name == name {
				target = path
				foundName = true
			}
		}
	}
	class := "other"
	if !foundName {
		if index := infer(candidates, name, folder); index >= 0 {
			candidate := candidates[index]
			target, parent = owners[index], candidate.Name
			// A book may omit class entirely (the probot book does); inheriting
			// "" would write an empty class into the new entry.
			if candidate.Class != "" {
				class = candidate.Class
			}
		}
		if reg.Parent != "" {
			parent = reg.Parent
		}
	}
	role := reg.Role
	if role == "" {
		role = "(new - add role)"
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
			// Explicit pins correct an existing entry in place — in its own
			// book, never as a second entry elsewhere.
			pinned := false
			if current, _ := agent["parent"].(string); reg.Parent != "" && current != reg.Parent {
				agent["parent"] = reg.Parent
				pinned = true
			}
			if current, _ := agent["role"].(string); reg.Role != "" && current != reg.Role {
				agent["role"] = reg.Role
				pinned = true
			}
			if currentStatus == status && currentFolder == folder && !pinned {
				return nil
			}
			agent["status"] = status
			found = true
			break
		}
	}
	if !found {
		agent := map[string]any{"name": name, "folder": folder, "class": class, "role": role, "status": status}
		if parent != "" {
			agent["parent"] = parent
		}
		agents = append(agents, agent)
	}
	raw["agents"] = agents
	raw["updated"] = time.Now().Format("2006-01-02")
	return writeBook(target, raw, info.Mode().Perm())
}

type State struct {
	Alive bool
	Busy  bool
	// ScreenBusy and TurnBusy are the two gates Busy is composed of, kept
	// separately because a composite that hides its components cannot be
	// diagnosed from outside. On 2026-08-18 an operator measured `bp status`
	// saying "working", attributed the answer to the SCREEN gate, and reported
	// the screen signature as drifted — when the answer had come from the
	// transcript gate all along. Exposing both lets anyone run the
	// distinguishing test themselves instead of measuring the composite and
	// guessing which component spoke.
	ScreenBusy bool
	TurnBusy   bool
	// Dead: the tmux session is up but its pane no longer runs an agent —
	// the CLI exited and left a bare shell behind. Not the same as idle.
	Dead bool
}

func LiveStates(ctx context.Context, client *bptmux.Client, fleet *Fleet) (map[string]State, error) {
	sessions, err := client.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	fleet.AddLive(sessions)
	commands, err := client.Commands(ctx)
	if err != nil {
		commands = nil // degrade to Dead=false rather than failing status
	}
	states := make(map[string]State, len(sessions))
	projectsRoot := bptmux.ClaudeProjectsRoot()
	now := time.Now()
	for _, name := range sessions {
		pane, captureErr := client.Capture(ctx, name)
		// Busy is the OR of both gates, so the column means the same thing here as
		// it does in the queue. The screen is asked first and settles most rows;
		// the transcript is only read when the screen says idle, which is what
		// makes a streaming turn — invisible on screen for minutes at a time —
		// show up as busy instead of as an agent waiting for work. The extra cost
		// is one stat per idle agent, and a bounded tail read only for the ones
		// whose session file was touched in the last quarter of an hour.
		screenBusy := captureErr == nil && bptmux.Busy(pane)
		// Both gates are evaluated even when the screen already said busy: the
		// per-gate fields in `bp status --json` exist precisely so the two can be
		// compared from outside, and a short-circuited TurnOpen would make the
		// comparison lie for every screen-busy row. The extra cost is one stat.
		turnBusy := TurnOpen(projectsRoot, fleet.Agents[name].Folder, name, now)
		states[name] = State{
			Alive:      true,
			Busy:       screenBusy || turnBusy,
			ScreenBusy: screenBusy,
			TurnBusy:   turnBusy,
			// Dead is decided on the command AND the screen. The screen half is
			// there for Hermes, whose pane reports "python" (measured 2026-08-22 in
			// blueprint-hermes-test): on the command alone every live Hermes agent
			// would be listed as a dead shell, and `bp status` saying "dead" about a
			// working agent is the kind of wrong that gets acted on. pane is empty
			// when the capture failed, and IsAgentPane then falls back to the
			// command — the answer this line used to give.
			Dead: commands != nil && !bptmux.IsAgentPane(commands[name], pane),
		}
	}
	return states, nil
}
