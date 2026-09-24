package book

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"blueprint/internal/cache"
)

const (
	LifetimeEphemeral  = "ephemeral"
	LifetimePersistent = "persistent"
)

var errNoLongerClosedEphemeral = errors.New("registration is no longer a closed ephemeral agent")

func (a Agent) IsEphemeral() bool { return a.Lifetime == LifetimeEphemeral }

// SetLifetime changes one active registration. An omitted lifetime in an old
// book is intentionally equivalent to persistent, but keep writes the explicit
// value so future migrations do not have to infer intent.
func SetLifetime(paths []string, name, lifetime string) error {
	if lifetime != LifetimeEphemeral && lifetime != LifetimePersistent {
		return fmt.Errorf("invalid lifetime: %s", lifetime)
	}
	records, err := Records(paths)
	if err != nil {
		return err
	}
	count := 0
	for _, record := range records {
		if record.Agent.Name == name && record.Agent.ArchivedAt == "" {
			count++
		}
	}
	if count == 0 {
		return fmt.Errorf("unknown agent: %s", name)
	}
	if count > 1 {
		return fmt.Errorf("%s has multiple active registrations", name)
	}
	return mutateActive(paths, name, func(agent map[string]any) bool {
		current, _ := agent["lifetime"].(string)
		if current == lifetime {
			return false
		}
		agent["lifetime"] = lifetime
		delete(agent, "lifecycleNote")
		return true
	})
}

// CheckArchive reports structural archive blockers without changing a book.
// SetArchived repeats these checks while holding the locks when applying.
func CheckArchive(paths []string, name string) error {
	records, err := Records(paths)
	if err != nil {
		return err
	}
	count := 0
	for _, record := range records {
		if record.Agent.Name == name {
			count++
		}
	}
	if count == 0 {
		return fmt.Errorf("unknown agent: %s", name)
	}
	if count > 1 {
		return fmt.Errorf("%s has multiple registrations", name)
	}
	fleet, err := LoadFleet(paths)
	if err != nil {
		return err
	}
	if name == fleet.Root {
		return fmt.Errorf("cannot archive or replace the coordinator %s", name)
	}
	for child, parent := range fleet.Parents {
		if parent == name {
			return fmt.Errorf("%s still has child %s; archive or reparent children first", name, child)
		}
	}
	return nil
}

func setLifecycleNote(paths []string, name, note string) error {
	return mutateActive(paths, name, func(agent map[string]any) bool {
		current, _ := agent["lifecycleNote"].(string)
		if current == note {
			return false
		}
		if note == "" {
			delete(agent, "lifecycleNote")
		} else {
			agent["lifecycleNote"] = note
		}
		return true
	})
}

// ArchiveClosedEphemerals applies the normal archive mutation to every closed
// ephemeral registration. A hierarchy refusal is preserved on the record and
// returned as a result instead of becoming a reason to bypass the child check.
func ArchiveClosedEphemerals(paths []string, guards ...func(Agent) error) ([]string, error) {
	records, err := Records(paths)
	if err != nil {
		return nil, err
	}
	var names []string
	seen := map[string]bool{}
	for _, record := range records {
		agent := record.Agent
		if agent.ArchivedAt == "" && agent.Status == "closed" && agent.IsEphemeral() && !seen[agent.Name] {
			seen[agent.Name] = true
			names = append(names, agent.Name)
		}
	}
	sort.Strings(names)
	var results []string
	for _, name := range names {
		check := func(agent Agent) error {
			if agent.Status != "closed" || !agent.IsEphemeral() {
				return errNoLongerClosedEphemeral
			}
			for _, guard := range guards {
				if guard != nil {
					if err := guard(agent); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := SetArchived(paths, name, true, check); err != nil {
			if errors.Is(err, errNoLongerClosedEphemeral) {
				results = append(results, name+": automatic archive skipped: "+err.Error())
				continue
			}
			note := "automatic archive skipped: " + err.Error()
			if noteErr := setLifecycleNote(paths, name, note); noteErr != nil {
				return results, errors.Join(err, noteErr)
			}
			results = append(results, name+": "+note)
			continue
		}
		results = append(results, name+": archived")
	}
	return results, nil
}

// TranscriptMissing is conservative: it returns true only when the record
// identifies a native transcript and that transcript no longer exists. Unknown
// bindings are not migration candidates.
func TranscriptMissing(agent Agent) bool {
	var candidates []string
	if agent.NativeTitle != nil && agent.NativeTitle.Path != "" && filepath.Base(filepath.Clean(agent.NativeTitle.Path)) != "session_index.jsonl" {
		candidates = append(candidates, agent.NativeTitle.Path)
	}
	if agent.Local != nil && agent.Local.Path != "" {
		if data, err := os.ReadFile(agent.Local.Path); err == nil {
			var observation cache.LocalObservation
			if json.Unmarshal(data, &observation) == nil && filepath.IsAbs(observation.TranscriptPath) {
				candidates = append(candidates, observation.TranscriptPath)
			}
		}
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	if len(candidates) > 0 {
		return true
	}

	// Codex stores the title index separately from rollout transcripts. An exact
	// thread ID lets migration prove absence by scanning that CODEX_HOME only.
	if agent.NativeTitle != nil && agent.NativeTitle.Path != "" && filepath.Base(filepath.Clean(agent.NativeTitle.Path)) == "session_index.jsonl" && agent.NativeTitle.ThreadID != "" {
		root := filepath.Join(filepath.Dir(agent.NativeTitle.Path), "sessions")
		found := false
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			if !entry.IsDir() && strings.Contains(entry.Name(), agent.NativeTitle.ThreadID) && strings.HasSuffix(entry.Name(), ".jsonl") {
				found = true
				return filepath.SkipAll
			}
			return nil
		})
		return err == nil && !found
	}
	return false
}
