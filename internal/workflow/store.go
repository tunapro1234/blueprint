package workflow

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Run struct {
	ID            string            `json:"id"`
	Workflow      string            `json:"workflow"`
	SnapshotHash  string            `json:"workflow_snapshot_hash"`
	Workdir       string            `json:"workdir"`
	Agents        []string          `json:"agents"`
	Limit         int               `json:"limit,omitempty"`
	RetryFailed   bool              `json:"retry_failed,omitempty"`
	UnitKeys      []string          `json:"unit_keys,omitempty"`
	Owner         string            `json:"owner"`
	Status        string            `json:"status"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	LastError     string            `json:"last_error,omitempty"`
	StopRequested bool              `json:"stop_requested,omitempty"`
	RestartCount  int               `json:"restart_count,omitempty"`
	Current       map[string]string `json:"current,omitempty"`
	AgentUnits    map[string]int    `json:"agent_units,omitempty"`
}

type Event struct {
	At              time.Time      `json:"at"`
	QueuedAt        *time.Time     `json:"queued_at,omitempty"`
	DeliveredAt     *time.Time     `json:"delivered_at,omitempty"`
	Unit            string         `json:"unit"`
	Key             string         `json:"key"`
	Agent           string         `json:"agent"`
	State           string         `json:"state"`
	Round           int            `json:"round,omitempty"`
	Reason          string         `json:"reason,omitempty"`
	DeliveryID      string         `json:"delivery_id,omitempty"`
	DeliveryStatus  string         `json:"delivery_status,omitempty"`
	ControlReplaced int            `json:"control_replaced,omitempty"`
	LastObservation Observation    `json:"last_observation,omitempty"`
	Compact         *CompactRecord `json:"compact,omitempty"`
}

type CompactRecord struct {
	Seconds float64 `json:"seconds"`
	Before  *int    `json:"ctx_before,omitempty"`
	After   *int    `json:"ctx_after,omitempty"`
	Result  string  `json:"result,omitempty"`
}

type TimeRow struct {
	Run              string     `json:"run"`
	Workflow         string     `json:"workflow"`
	Unit             string     `json:"unit"`
	Agent            string     `json:"agent"`
	Model            string     `json:"model,omitempty"`
	Result           string     `json:"result"`
	Reason           string     `json:"reason,omitempty"`
	Rounds           int        `json:"rounds"`
	RoundSeconds     []float64  `json:"round_s,omitempty"`
	QueuedAt         *time.Time `json:"queued_at,omitempty"`
	DeliveredAt      *time.Time `json:"delivered_at,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
	FinishedAt       time.Time  `json:"finished_at"`
	WaitSeconds      float64    `json:"wait_s,omitempty"`
	WorkSeconds      float64    `json:"work_s,omitempty"`
	ValidateSeconds  float64    `json:"validate_s,omitempty"`
	CompactSeconds   float64    `json:"compact_s,omitempty"`
	CompactResult    string     `json:"compact_result,omitempty"`
	IdleGapSeconds   *float64   `json:"idle_gap_s,omitempty"`
	CtxBefore        *int       `json:"ctx_before,omitempty"`
	CtxAfter         *int       `json:"ctx_after,omitempty"`
	CtxStart         *int       `json:"ctx_start,omitempty"`
	CtxEnd           *int       `json:"ctx_end,omitempty"`
	CompactCtxBefore *int       `json:"compact_ctx_before,omitempty"`
	CompactCtxAfter  *int       `json:"compact_ctx_after,omitempty"`
}

type Store struct {
	StateDir string
	Now      func() time.Time
	mu       sync.Mutex
}

func NewStore(stateDir string) *Store { return &Store{StateDir: stateDir, Now: time.Now} }

func (s *Store) WorkflowsDir() string            { return filepath.Join(s.StateDir, "workflows") }
func (s *Store) RunsDir() string                 { return filepath.Join(s.StateDir, "workflow-runs") }
func (s *Store) WorkflowPath(name string) string { return filepath.Join(s.WorkflowsDir(), name) }
func (s *Store) RunPath(id string) string        { return filepath.Join(s.RunsDir(), id) }

func (s *Store) Add(source string, replace bool) (Definition, string, error) {
	d, err := ReadDefinition(source)
	if err != nil {
		return Definition{}, "", err
	}
	if info, statErr := os.Stat(filepath.Join(source, filepath.FromSlash(d.Units.File))); statErr == nil && !info.IsDir() {
		if err := CheckSampleTemplates(source, source, d); err != nil {
			return Definition{}, "", err
		}
	}
	hash, err := HashTree(source)
	if err != nil {
		return Definition{}, "", err
	}
	if err := os.MkdirAll(s.WorkflowsDir(), 0700); err != nil {
		return Definition{}, "", err
	}
	destination := s.WorkflowPath(d.Name)
	if old, err := HashTree(destination); err == nil {
		if old == hash {
			return d, hash, nil
		}
		if !replace {
			return Definition{}, "", fmt.Errorf("workflow %q already exists with different content; use --replace", d.Name)
		}
	} else if !os.IsNotExist(err) {
		return Definition{}, "", err
	}
	tmp, err := os.MkdirTemp(s.WorkflowsDir(), ".workflow-add-")
	if err != nil {
		return Definition{}, "", err
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(source, tmp); err != nil {
		return Definition{}, "", err
	}
	if _, err := ReadDefinition(tmp); err != nil {
		return Definition{}, "", err
	}
	if _, err := os.Stat(destination); err == nil {
		backup := destination + ".replace-" + randomSuffix()
		if err := os.Rename(destination, backup); err != nil {
			return Definition{}, "", err
		}
		if err := os.Rename(tmp, destination); err != nil {
			_ = os.Rename(backup, destination)
			return Definition{}, "", err
		}
		if err := os.RemoveAll(backup); err != nil {
			return Definition{}, "", err
		}
	} else if err := os.Rename(tmp, destination); err != nil {
		return Definition{}, "", err
	}
	return d, hash, nil
}

func (s *Store) List() ([]Definition, error) {
	entries, err := os.ReadDir(s.WorkflowsDir())
	if os.IsNotExist(err) {
		return []Definition{}, nil
	}
	if err != nil {
		return nil, err
	}
	var definitions []Definition
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		d, err := ReadDefinition(filepath.Join(s.WorkflowsDir(), entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("workflow %s: %w", entry.Name(), err)
		}
		definitions = append(definitions, d)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	return definitions, nil
}

func (s *Store) ResolveWorkflow(value string) (string, Definition, error) {
	path := value
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		if !workflowNamePattern.MatchString(value) {
			return "", Definition{}, fmt.Errorf("workflow %q is neither a directory nor a saved workflow name", value)
		}
		path = s.WorkflowPath(value)
	}
	d, err := ReadDefinition(path)
	return path, d, err
}

func (s *Store) CreateRun(source string, d Definition, workdir string, agents []string, owner string) (Run, error) {
	workdir, err := filepath.Abs(workdir)
	if err != nil {
		return Run{}, err
	}
	info, err := os.Stat(workdir)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("workdir is not a directory")
		}
		return Run{}, fmt.Errorf("workflow workdir: %w", err)
	}
	units, err := LoadUnits(d, workdir)
	if err != nil {
		return Run{}, err
	}
	if err := CheckSampleTemplates(source, workdir, d); err != nil {
		return Run{}, err
	}
	if len(units) == 0 {
		return Run{}, errors.New("units file contains no units")
	}
	hash, err := HashTree(source)
	if err != nil {
		return Run{}, err
	}
	if err := os.MkdirAll(s.RunsDir(), 0700); err != nil {
		return Run{}, err
	}
	id := s.newRunID()
	runDir := s.RunPath(id)
	tmp, err := os.MkdirTemp(s.RunsDir(), ".run-create-")
	if err != nil {
		return Run{}, err
	}
	defer os.RemoveAll(tmp)
	if err := os.Mkdir(filepath.Join(tmp, "workflow"), 0700); err != nil {
		return Run{}, err
	}
	if err := copyTree(source, filepath.Join(tmp, "workflow")); err != nil {
		return Run{}, err
	}
	for _, name := range []string{"units.jsonl", "times.jsonl"} {
		f, err := os.OpenFile(filepath.Join(tmp, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return Run{}, err
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return Run{}, err
		}
		if err := f.Close(); err != nil {
			return Run{}, err
		}
	}
	now := s.now()
	keys := make([]string, len(units))
	for i, unit := range units {
		keys[i] = unit.Key
	}
	run := Run{ID: id, Workflow: d.Name, SnapshotHash: hash, Workdir: workdir, Agents: append([]string(nil), agents...), UnitKeys: keys, Owner: owner, Status: "running", CreatedAt: now, UpdatedAt: now, Current: map[string]string{}}
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return Run{}, err
	}
	data = append(data, '\n')
	if err := writeSynced(filepath.Join(tmp, "run.json"), data, 0600); err != nil {
		return Run{}, err
	}
	if err := os.Rename(tmp, runDir); err != nil {
		return Run{}, err
	}
	if err := syncDir(s.RunsDir()); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (s *Store) LoadRun(id string) (Run, error) {
	if !validRunID(id) {
		return Run{}, fmt.Errorf("invalid workflow run id %q", id)
	}
	data, err := os.ReadFile(filepath.Join(s.RunPath(id), "run.json"))
	if err != nil {
		return Run{}, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return Run{}, err
	}
	if run.ID != id {
		return Run{}, errors.New("workflow run id does not match its directory")
	}
	return run, nil
}

// UpdateRun serializes read-modify-write updates across Store instances and
// processes. Callers should mutate only the fields they own.
func (s *Store) UpdateRun(id string, update func(*Run) error) (Run, error) {
	if !validRunID(id) {
		return Run{}, fmt.Errorf("invalid workflow run id %q", id)
	}
	if update == nil {
		return Run{}, errors.New("workflow run update is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runDir := s.RunPath(id)
	lock, err := os.OpenFile(filepath.Join(runDir, "run.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Run{}, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Run{}, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	run, err := s.LoadRun(id)
	if err != nil {
		return Run{}, err
	}
	if err := update(&run); err != nil {
		return Run{}, err
	}
	run.UpdatedAt = s.now()
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return Run{}, err
	}
	if err := atomicWrite(filepath.Join(runDir, "run.json"), append(data, '\n'), 0600); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (s *Store) RequestStop(id string) (Run, error) {
	return s.UpdateRun(id, func(run *Run) error {
		if run.Status == "done" || run.Status == "stopped" || run.Status == "error" {
			return nil
		}
		run.StopRequested, run.Status = true, "stopping"
		return nil
	})
}

func (s *Store) AppendEvent(id string, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.At.IsZero() {
		event.At = s.now()
	}
	return appendSynced(filepath.Join(s.RunPath(id), "units.jsonl"), event)
}

func (s *Store) AppendTime(id string, row TimeRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return appendSynced(filepath.Join(s.RunPath(id), "times.jsonl"), row)
}

func (s *Store) Events(id string) ([]Event, bool, error) {
	return readJSONLines[Event](filepath.Join(s.RunPath(id), "units.jsonl"))
}

func (s *Store) Times(id string) ([]TimeRow, bool, error) {
	return readJSONLines[TimeRow](filepath.Join(s.RunPath(id), "times.jsonl"))
}

func (s *Store) Runs() ([]Run, error) {
	entries, err := os.ReadDir(s.RunsDir())
	if os.IsNotExist(err) {
		return []Run{}, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []Run
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		run, err := s.LoadRun(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read workflow run %s: %w", entry.Name(), err)
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.Before(runs[j].CreatedAt) })
	return runs, nil
}

func (s *Store) SnapshotDir(id string) string { return filepath.Join(s.RunPath(id), "workflow") }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) newRunID() string {
	return s.now().Format("20060102T150405Z") + "-" + randomSuffix()
}

func randomSuffix() string {
	var raw [5]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%010x", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

func validRunID(value string) bool {
	if value == "" || strings.Contains(value, "..") || filepath.Base(value) != value {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func HashTree(root string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	var names []string
	err = filepath.WalkDir(rootAbs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("workflow symlink is not allowed: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("workflow contains a non-regular file: %s", path)
		}
		rel, err := filepath.Rel(rootAbs, path)
		if err != nil {
			return err
		}
		names = append(names, rel)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		if _, err := io.WriteString(hash, filepath.ToSlash(name)+"\x00"); err != nil {
			return "", err
		}
		file, err := os.Open(filepath.Join(rootAbs, name))
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(hash, file); err != nil {
			_ = file.Close()
			return "", err
		}
		if err := file.Close(); err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entry.Type().IsRegular()) {
			return fmt.Errorf("workflow contains unsupported file %s", rel)
		}
		target := filepath.Join(destination, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		mode := os.FileMode(0600)
		if info, err := entry.Info(); err != nil {
			_ = in.Close()
			return err
		} else if info.Mode().Perm()&0100 != 0 {
			mode |= 0100
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		syncErr := out.Sync()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	})
}

func appendSynced(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	if info.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
			_ = f.Close()
			return err
		}
		if last[0] != '\n' {
			data = append([]byte{'\n'}, data...)
		}
	}
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func readJSONLines[T any](path string) ([]T, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	torn := len(data) > 0 && data[len(data)-1] != '\n'
	if torn {
		if last := bytesLastIndexByte(data, '\n'); last < 0 {
			data = nil
		} else {
			data = data[:last+1]
		}
	}
	var result []T
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var item T
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			// A process can die after writing only part of one JSON record. Appends
			// separate such a fragment from the next record; ignore it while
			// surfacing TornLog to status instead of making the run unrecoverable.
			torn = true
			continue
		}
		result = append(result, item)
	}
	return result, torn, nil
}

func bytesLastIndexByte(data []byte, b byte) int {
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == b {
			return i
		}
	}
	return -1
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".workflow-state-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func writeSynced(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
