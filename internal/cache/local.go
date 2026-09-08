package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// LocalBinding is created by bp's launcher, not inferred from cwd or TMUX env.
// It is telemetry only and never grants a sender identity or hierarchy rights.
type LocalBinding struct {
	Home    string `json:"home,omitempty"`
	CWD     string `json:"cwd,omitempty"`
	Path    string `json:"path"`
	PID     int    `json:"pid"`
	Harness string `json:"harness"`
}

type LocalObservation struct {
	SessionID      string    `json:"session_id"`
	TranscriptPath string    `json:"transcript_path"`
	CWD            string    `json:"cwd"`
	Model          string    `json:"model,omitempty"`
	Window         int       `json:"window,omitempty"`
	Context        *int      `json:"context,omitempty"`
	ObservedAt     time.Time `json:"observed_at"`
}

var localSessionID = regexp.MustCompile(`^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

func ReadLocalObservation(binding *LocalBinding, pid int) (LocalObservation, error) {
	var observation LocalObservation
	if binding == nil || binding.PID <= 0 || binding.PID != pid {
		return observation, fmt.Errorf("local launch PID no longer matches pane")
	}
	data, err := os.ReadFile(binding.Path)
	if err != nil {
		return observation, fmt.Errorf("waiting for %s session observation (reopen older bp sessions): %w", binding.Harness, err)
	}
	if err := json.Unmarshal(data, &observation); err != nil {
		return observation, fmt.Errorf("invalid local session observation")
	}
	if !localSessionID.MatchString(observation.SessionID) || !filepath.IsAbs(observation.CWD) || !filepath.IsAbs(observation.TranscriptPath) || observation.ObservedAt.IsZero() {
		return observation, fmt.Errorf("incomplete local session observation")
	}
	return observation, nil
}
