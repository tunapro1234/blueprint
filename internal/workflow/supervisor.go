package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const controlSocketName = "workflow-control.sock"

func ControlSocketPath(stateDir string) string { return filepath.Join(stateDir, controlSocketName) }

// Control sends one request through the daemon's private Unix socket.
func Control(ctx context.Context, stateDir, method, endpoint string, request, result any) error {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", ControlSocketPath(stateDir))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	var body io.Reader
	if request != nil {
		data, err := json.Marshal(request)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://bp/"+strings.TrimPrefix(endpoint, "/"), body)
	if err != nil {
		return err
	}
	if request != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("workflow daemon is not running or reachable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("workflow daemon: %s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(result)
	}
	return nil
}

func Start(ctx context.Context, stateDir, runID string) error {
	return Control(ctx, stateDir, http.MethodPost, "/start", map[string]string{"run_id": runID}, nil)
}

func CheckDaemon(ctx context.Context, stateDir string) error {
	return Control(ctx, stateDir, http.MethodGet, "/health", nil, nil)
}

type ChildWait func() error
type ChildLauncher func(context.Context, string) (ChildWait, error)
type DelayFunc func(context.Context, time.Duration) error

type Supervisor struct {
	Store       *Store
	StateDir    string
	Launch      ChildLauncher
	Delay       DelayFunc
	Logger      *log.Logger
	MaxRestarts int

	mu     sync.Mutex
	active map[string]bool
	agents map[string]string
	locks  map[string]*os.File
	wg     sync.WaitGroup
	ctx    context.Context
}

func NewSupervisor(store *Store, stateDir string, launch ChildLauncher, logger *log.Logger) *Supervisor {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Supervisor{Store: store, StateDir: stateDir, Launch: launch, Delay: sleepContext, Logger: logger, MaxRestarts: 6,
		active: make(map[string]bool), agents: make(map[string]string), locks: make(map[string]*os.File), ctx: context.Background()}
}

// CommandLauncher starts the hidden worker in a separate process. Engine panics
// and fatal exits therefore cannot unwind through the daemon.
func CommandLauncher(executable, stateDir string) ChildLauncher {
	return func(ctx context.Context, runID string) (ChildWait, error) {
		store := NewStore(stateDir)
		run, err := store.LoadRun(runID)
		if err != nil {
			return nil, err
		}
		logFile, err := os.OpenFile(filepath.Join(store.RunPath(runID), "worker.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		args := []string{"_workflow-run", runID}
		if run.RetryFailed {
			args = append(args, "--retry-failed")
		}
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, logFile, logFile
		if err := cmd.Start(); err != nil {
			_ = logFile.Close()
			return nil, err
		}
		return func() error {
			err := cmd.Wait()
			closeErr := logFile.Close()
			if err != nil {
				return err
			}
			return closeErr
		}, nil
	}
}

func (s *Supervisor) Serve(ctx context.Context) error {
	if s.Store == nil || s.Launch == nil {
		return errors.New("workflow supervisor needs a store and child launcher")
	}
	if err := os.MkdirAll(s.StateDir, 0700); err != nil {
		return err
	}
	guard, err := os.OpenFile(filepath.Join(s.StateDir, ".workflow-supervisor.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = guard.Close()
		return fmt.Errorf("workflow supervisor already running: %w", err)
	}
	defer func() {
		_ = syscall.Flock(int(guard.Fd()), syscall.LOCK_UN)
		_ = guard.Close()
	}()
	socket := ControlSocketPath(s.StateDir)
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket %s", socket)
		}
		if err := os.Remove(socket); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socket)
	}()
	if err := os.Chmod(socket, 0600); err != nil {
		return err
	}
	s.ctx = ctx
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/start", s.handleStart)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	if err := s.restore(); err != nil {
		s.Logger.Printf("workflow startup recovery: %v", err)
	}
	select {
	case <-ctx.Done():
	case err := <-serveDone:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = server.Close()
			s.wg.Wait()
			return err
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	s.wg.Wait()
	return nil
}

func (s *Supervisor) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		RunID string `json:"run_id"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := s.Start(request.RunID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Supervisor) restore() error {
	runs, err := s.Store.Runs()
	if err != nil {
		return err
	}
	var failures []error
	for _, run := range runs {
		if resumableStatus(run.Status) {
			if err := s.Start(run.ID); err != nil {
				failures = append(failures, fmt.Errorf("run %s: %w", run.ID, err))
			}
		}
	}
	return errors.Join(failures...)
}

func resumableStatus(status string) bool {
	return status == "running" || status == "stopping" || strings.HasPrefix(status, "waiting:")
}

func (s *Supervisor) Start(id string) error {
	if !validRunID(id) {
		return fmt.Errorf("invalid workflow run id %q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[id] {
		return nil
	}
	run, err := s.Store.LoadRun(id)
	if err != nil {
		return err
	}
	if !resumableStatus(run.Status) {
		return fmt.Errorf("workflow run %s is not resumable (status %s)", id, run.Status)
	}
	var acquired []*os.File
	for _, agent := range run.Agents {
		if other := s.agents[agent]; other != "" && other != id {
			closeLocks(acquired)
			return fmt.Errorf("agent %s is already used by workflow run %s", agent, other)
		}
		if s.agents[agent] == id {
			continue
		}
		file, err := s.lockAgent(agent, id)
		if err != nil {
			closeLocks(acquired)
			return err
		}
		acquired = append(acquired, file)
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	wait, err := s.Launch(ctx, id)
	if err != nil {
		closeLocks(acquired)
		return fmt.Errorf("start workflow child: %w", err)
	}
	for index, agent := range run.Agents {
		if s.agents[agent] == "" && index < len(acquired) {
			s.agents[agent], s.locks[agent] = id, acquired[index]
		}
	}
	s.active[id] = true
	s.wg.Add(1)
	go s.supervise(ctx, id, wait)
	return nil
}

func (s *Supervisor) supervise(ctx context.Context, id string, wait ChildWait) {
	defer s.wg.Done()
	defer s.releaseRun(id)
	defer func() {
		if recovered := recover(); recovered != nil {
			s.markError(id, fmt.Sprintf("workflow supervisor panic: %v", recovered))
			s.Logger.Printf("workflow supervisor recovered panic for %s: %v", id, recovered)
		}
	}()
	for {
		err := wait()
		if ctx.Err() != nil {
			return
		}
		run, loadErr := s.Store.LoadRun(id)
		if loadErr != nil {
			s.Logger.Printf("workflow %s: read state after child exit: %v", id, loadErr)
			return
		}
		if !resumableStatus(run.Status) {
			return
		}
		if run.RestartCount >= s.maxRestarts() {
			s.markError(id, fmt.Sprintf("workflow child exited unexpectedly %d times (last exit: %v)", run.RestartCount, err))
			return
		}
		run.RestartCount++
		if saveErr := s.Store.SaveRun(run); saveErr != nil {
			s.Logger.Printf("workflow %s: record restart count: %v", id, saveErr)
			return
		}
		if err := s.delay(ctx, s.restartDelay(run.RestartCount)); err != nil {
			return
		}
		for {
			wait, err = s.Launch(ctx, id)
			if err == nil {
				break
			}
			s.Logger.Printf("workflow %s: restart child: %v", id, err)
			run, loadErr := s.Store.LoadRun(id)
			if loadErr != nil {
				return
			}
			if run.RestartCount >= s.maxRestarts() {
				s.markError(id, fmt.Sprintf("workflow child could not be restarted %d times (last error: %v)", run.RestartCount, err))
				return
			}
			run.RestartCount++
			if saveErr := s.Store.SaveRun(run); saveErr != nil {
				s.Logger.Printf("workflow %s: record restart count: %v", id, saveErr)
				return
			}
			if delayErr := s.delay(ctx, s.restartDelay(run.RestartCount)); delayErr != nil {
				return
			}
		}
	}
}

func (s *Supervisor) maxRestarts() int {
	if s.MaxRestarts >= 0 {
		return s.MaxRestarts
	}
	return 6
}

func (s *Supervisor) restartDelay(restart int) time.Duration {
	delay := time.Second << max(0, restart-1)
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

func (s *Supervisor) delay(ctx context.Context, duration time.Duration) error {
	if s.Delay != nil {
		return s.Delay(ctx, duration)
	}
	return sleepContext(ctx, duration)
}

func (s *Supervisor) lockAgent(agent, runID string) (*os.File, error) {
	digest := sha256.Sum256([]byte(agent))
	dir := filepath.Join(s.StateDir, "workflow-agent-locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, hex.EncodeToString(digest[:])[:24]+".lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("agent %s is already locked by another workflow supervisor", agent)
	}
	if err := file.Truncate(0); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.WriteString(runID + "\n"); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func closeLocks(files []*os.File) {
	for _, file := range files {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}
}

func (s *Supervisor) releaseRun(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for agent, owner := range s.agents {
		if owner == id {
			file := s.locks[agent]
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			_ = file.Close()
			delete(s.locks, agent)
			delete(s.agents, agent)
		}
	}
	delete(s.active, id)
}

func (s *Supervisor) markError(id, reason string) {
	run, err := s.Store.LoadRun(id)
	if err != nil {
		return
	}
	run.Status, run.LastError = "error", reason
	_ = s.Store.SaveRun(run)
}
