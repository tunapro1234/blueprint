package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type JobState struct {
	LastRun  string `json:"last_run,omitempty"`
	Status   string `json:"status"`
	NextRun  string `json:"next_run,omitempty"`
	Duration string `json:"duration,omitempty"`
	Error    string `json:"error,omitempty"`
}

type State struct {
	Updated string              `json:"updated"`
	Jobs    map[string]JobState `json:"jobs"`
	mu      sync.Mutex
	path    string
}

func NewState(path string) *State {
	return &State{path: path, Jobs: map[string]JobState{}}
}

func (s *State) update(name string, value JobState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Jobs[name] = value
	s.Updated = time.Now().Format(time.RFC3339)
	return s.saveLocked()
}

func (s *State) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".jobs-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	copyState := struct {
		Updated string              `json:"updated"`
		Jobs    map[string]JobState `json:"jobs"`
	}{s.Updated, s.Jobs}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(copyState)
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

func LoadState(path string) (map[string]JobState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state struct {
		Jobs map[string]JobState `json:"jobs"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return state.Jobs, nil
}

func StateNames(jobs map[string]JobState) []string {
	names := make([]string, 0, len(jobs))
	for name := range jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
