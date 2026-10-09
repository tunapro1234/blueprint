package modules

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"blueprint/internal/config"
)

// Change kinds recorded in a journal.
const (
	// KindFile is a file bp wrote. Undo removes it while it still contains
	// Marker; a file without the marker is the user's and stays.
	KindFile = "file"
	// KindLine is one line bp appended to a user file such as ~/.zshrc. Undo
	// removes that exact line wherever it is.
	KindLine = "line"
	// KindLink is a symlink bp created. Undo removes it while it still points
	// at Target.
	KindLink = "link"
	// KindTmuxOption is a tmux option bp set. Undo restores Previous while
	// the option still holds Value.
	KindTmuxOption = "tmux-option"
	// KindNote is something bp cannot undo by itself; uninstall prints it.
	KindNote = "note"
)

// Change is one recorded environment change.
type Change struct {
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	Marker   string `json:"marker,omitempty"`
	Line     string `json:"line,omitempty"`
	Target   string `json:"target,omitempty"`
	Option   string `json:"option,omitempty"`
	Value    string `json:"value,omitempty"`
	Previous string `json:"previous,omitempty"`
	Unset    bool   `json:"unset,omitempty"`
	Text     string `json:"text,omitempty"`
	At       string `json:"at,omitempty"`
}

func (c Change) key() string {
	return strings.Join([]string{c.Kind, c.Path, c.Line, c.Target, c.Option, c.Text}, "\x00")
}

func (c Change) String() string {
	switch c.Kind {
	case KindFile:
		return "file " + c.Path
	case KindLine:
		return fmt.Sprintf("line in %s: %s", c.Path, c.Line)
	case KindLink:
		return fmt.Sprintf("link %s -> %s", c.Path, c.Target)
	case KindTmuxOption:
		target := "global"
		if c.Target != "" {
			target = c.Target
		}
		return fmt.Sprintf("tmux option %s (%s) = %s", c.Option, target, c.Value)
	case KindNote:
		return "note: " + c.Text
	}
	return c.Kind
}

// Journal is the ordered list of changes one module (or the installer, under
// the name "install") made.
type Journal struct {
	Module  string   `json:"module"`
	Changes []Change `json:"changes"`
	dryRun  bool
	planned []string
}

func journalPath(cfg config.Config, name string) string {
	return filepath.Join(journalDir(cfg), name+".json")
}

// LoadJournal reads a module's journal; a missing file is an empty journal.
func LoadJournal(cfg config.Config, name string) (*Journal, error) {
	journal := &Journal{Module: name}
	if cfg.StateDir == "" {
		return journal, nil
	}
	data, err := os.ReadFile(journalPath(cfg, name))
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, journal); err != nil {
		return nil, fmt.Errorf("read %s: %w", journalPath(cfg, name), err)
	}
	journal.Module = name
	return journal, nil
}

// Save writes the journal atomically; an empty journal removes the file.
func (j *Journal) Save(cfg config.Config) error {
	if j.dryRun {
		return nil
	}
	if len(j.Changes) == 0 {
		return j.Remove(cfg)
	}
	if err := os.MkdirAll(journalDir(cfg), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(journalPath(cfg, j.Module), append(data, '\n'), 0600)
}

// Remove deletes the journal file.
func (j *Journal) Remove(cfg config.Config) error {
	if j.dryRun || cfg.StateDir == "" {
		return nil
	}
	err := os.Remove(journalPath(cfg, j.Module))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Record adds a change unless an identical one is already recorded.
func (j *Journal) Record(change Change) {
	if j.dryRun {
		j.planned = append(j.planned, change.String())
		return
	}
	for _, existing := range j.Changes {
		if existing.key() == change.key() {
			return
		}
	}
	if change.At == "" {
		change.At = time.Now().UTC().Format(time.RFC3339)
	}
	j.Changes = append(j.Changes, change)
}

// DryRun reports whether hooks must only describe their changes.
func (j *Journal) DryRun() bool { return j.dryRun }

func (j *Journal) describe() []string {
	if j.dryRun {
		return append([]string(nil), j.planned...)
	}
	return j.describeFrom(0)
}

func (j *Journal) describeFrom(start int) []string {
	var lines []string
	for _, change := range j.Changes[start:] {
		lines = append(lines, change.String())
	}
	return lines
}

// Undo reverts every change, newest first. It returns what it did and what it
// left in place because the user changed it since.
func (j *Journal) Undo(env Env) (done, kept []string) {
	for index := len(j.Changes) - 1; index >= 0; index-- {
		change := j.Changes[index]
		action, err := undo(env, change)
		switch {
		case err != nil:
			kept = append(kept, fmt.Sprintf("%s (%v)", change, err))
		case action != "":
			done = append(done, action)
		}
	}
	j.Changes = nil
	return done, kept
}

var errModified = errors.New("changed since bp wrote it; left in place")

func undo(env Env, change Change) (string, error) {
	switch change.Kind {
	case KindFile:
		data, err := os.ReadFile(change.Path)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if change.Marker == "" || !bytes.Contains(data, []byte(change.Marker)) {
			return "", errModified
		}
		if err := os.Remove(change.Path); err != nil {
			return "", err
		}
		removeEmptyDir(filepath.Dir(change.Path))
		return "removed " + change.Path, nil
	case KindLine:
		removed, err := removeLine(change.Path, change.Line)
		if err != nil || !removed {
			return "", err
		}
		return fmt.Sprintf("removed line from %s", change.Path), nil
	case KindLink:
		target, err := os.Readlink(change.Path)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		if err != nil || target != change.Target {
			return "", errModified
		}
		if err := os.Remove(change.Path); err != nil {
			return "", err
		}
		return "removed " + change.Path, nil
	case KindTmuxOption:
		if env.Tmux == nil {
			return "", errors.New("tmux is not available")
		}
		scope := []string{"-g"}
		if change.Target != "" {
			scope = []string{"-t", change.Target}
		}
		current, err := env.Tmux(append(append([]string{"show-options", "-v"}, scope...), change.Option)...)
		if err != nil {
			// The target session is gone: nothing left to undo.
			if change.Target != "" {
				return "", nil
			}
			return "", err
		}
		if strings.TrimRight(current, "\n") != change.Value {
			return "", errModified
		}
		if change.Unset {
			_, err = env.Tmux(append(append([]string{"set-option", "-u"}, scope...), change.Option)...)
		} else {
			_, err = env.Tmux(append(append([]string{"set-option"}, scope...), change.Option, change.Previous)...)
		}
		if err != nil {
			return "", err
		}
		return "restored tmux option " + change.Option, nil
	case KindNote:
		return "", errors.New(change.Text)
	}
	return "", fmt.Errorf("unknown change kind %q", change.Kind)
}

// removeLine deletes every line equal to line, and one blank line directly
// before each when bp's append added it. The file keeps its mode.
func removeLine(path, line string) (bool, error) {
	// Rewrite the real file so a dotfile-manager symlink stays a symlink.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	lines := strings.SplitAfter(string(data), "\n")
	var out []string
	removed := false
	for _, current := range lines {
		if strings.TrimRight(current, "\r\n") == line {
			removed = true
			if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" {
				out = out[:n-1]
			}
			continue
		}
		out = append(out, current)
	}
	if !removed {
		return false, nil
	}
	return true, writeFileAtomic(path, []byte(strings.Join(out, "")), info.Mode().Perm())
}

func removeEmptyDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Install is the journal name for what the installer and bp setup add outside
// any module (agent hint files, the binary link). bp uninstall undoes it last.
const Install = "install"

// RecordInstall adds changes to the install journal.
func RecordInstall(cfg config.Config, changes ...Change) error {
	if len(changes) == 0 || cfg.StateDir == "" {
		return nil
	}
	unlock, err := lock(cfg)
	if err != nil {
		return err
	}
	defer unlock()
	journal, err := LoadJournal(cfg, Install)
	if err != nil {
		return err
	}
	for _, change := range changes {
		journal.Record(change)
	}
	return journal.Save(cfg)
}
