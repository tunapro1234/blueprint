package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/config"
	"blueprint/internal/dashboard"
	"blueprint/internal/fed"
	"blueprint/internal/msgq"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/tokens"
)

type Service struct {
	config config.Config
	state  *State
	tmux   *bptmux.Client
	queue  *msgq.Queue
	log    *log.Logger
	wg     sync.WaitGroup
}

func New(logger *log.Logger, cfg config.Config) *Service {
	if logger == nil {
		logger = log.New(os.Stderr, "blueprint: ", log.LstdFlags)
	}
	return &Service{config: cfg, state: NewState(filepath.Join(cfg.StateDir, "jobs.json")), tmux: bptmux.New(), queue: msgq.New(cfg.MsgqRoot), log: logger}
}

func (s *Service) Run(ctx context.Context) {
	s.startFederation(ctx)
	s.startLoop(ctx, "msgq", 5*time.Second, 30*time.Second, func(run context.Context, interval time.Duration) {
		s.tracked(run, "msgq", interval, func() error {
			return s.queue.Dispatch(run, s.tmux, func(message string) { s.log.Print(message) })
		})
	})
	s.startLoop(ctx, "keepalive", 30*time.Second, 2*time.Minute, func(run context.Context, interval time.Duration) {
		s.tracked(run, "keepalive", interval, func() error { return s.keepalive(run) })
	})
	if path, ok := s.usageBinary("usage-policy"); ok {
		s.startLoop(ctx, "usage-policy", time.Minute, 10*time.Minute, func(run context.Context, interval time.Duration) {
			s.tracked(run, "usage-policy", interval, func() error {
				return command(run, path)
			})
		})
	}
	if path, ok := s.usageBinary("usage-pulse"); ok {
		s.startLoop(ctx, "usage-pulse-chain", 90*time.Second, 5*time.Minute, func(run context.Context, interval time.Duration) {
			steps := []struct {
				name string
				args []string
			}{
				{"usage-pulse", []string{path}},
				{"dashboard-gen", []string{"/usr/bin/python3", "/srv/monitor/site/gen.py"}},
			}
			for _, step := range steps {
				if run.Err() != nil {
					return
				}
				// Dashboard generation must still run when the pulse command fails.
				_ = s.tracked(run, step.name, interval, func() error { return command(run, step.args...) })
			}
		})
	}
	if path, ok := s.usageBinary("usage-watch"); ok {
		s.startLoop(ctx, "usage-watch", 2*time.Minute, 10*time.Minute, func(run context.Context, interval time.Duration) {
			s.tracked(run, "usage-watch", interval, func() error { return command(run, path) })
		})
	}
	s.startLoop(ctx, "tokens-collect", 2*time.Minute, 5*time.Minute, func(run context.Context, interval time.Duration) {
		s.tracked(run, "tokens-collect", interval, func() error {
			config := tokens.DefaultConfig(s.config.StateDir, s.config.Agentbooks)
			config.Log = s.log.Writer()
			_, err := tokens.Collect(config)
			return err
		})
	})
	s.startLoop(ctx, "watch-radar-chain", 3*time.Minute, 30*time.Minute, func(run context.Context, interval time.Duration) {
		steps := []struct {
			name string
			args []string
		}{
			{"watch-radar", []string{"/usr/bin/python3", "/srv/monitor/watch/model_watch.py"}},
			{"watch-reactions", []string{"/usr/bin/python3", "/srv/monitor/watch/reactions.py"}},
		}
		for _, step := range steps {
			if run.Err() != nil {
				return
			}
			if err := s.tracked(run, step.name, interval, func() error {
				return commandDirEnv(run, "/srv/monitor/watch", []string{"AGENT=blueprint"}, step.args...)
			}); err != nil {
				return
			}
		}
	})
	s.startLoop(ctx, "watch-reset", 150*time.Second, 10*time.Minute, func(run context.Context, interval time.Duration) {
		s.tracked(run, "watch-reset", interval, func() error {
			return commandDirEnv(run, "/srv/monitor/watch", []string{"AGENT=blueprint"}, "/usr/bin/python3", "/srv/monitor/watch/reset_watch.py")
		})
	})
	// Serve the owner's dashboard on loopback so nginx can proxy monitor.tunapro.xyz to it.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for ctx.Err() == nil {
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						s.setState("dash-server", JobState{LastRun: time.Now().Format(time.RFC3339), Status: "failed", Error: fmt.Sprintf("panic: %v", recovered)})
					}
				}()
				s.setState("dash-server", JobState{LastRun: time.Now().Format(time.RFC3339), Status: "running"})
				if err := dashboard.Serve(ctx, dashboard.Options{Port: 8787, UsageBin: s.config.UsageBin, Open: func(string) {}}); err != nil && ctx.Err() == nil {
					s.setState("dash-server", JobState{LastRun: time.Now().Format(time.RFC3339), Status: "failed", Error: err.Error()})
					s.log.Printf("dash-server: %v", err)
				}
			}()
			if !wait(ctx, 5*time.Second) {
				return
			}
		}
	}()
	bridgePath := ""
	if s.config.WABridge && s.config.WAOutbox != "" {
		candidate := filepath.Join(filepath.Dir(s.config.WAOutbox), "bridge.js")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			bridgePath = candidate
		}
	}
	if bridgePath == "" {
		s.log.Print("skipping wa-bridge: not configured")
	} else {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for ctx.Err() == nil {
				panicked := false
				func() {
					defer func() {
						if recovered := recover(); recovered != nil {
							panicked = true
							s.setState("wa-bridge", JobState{LastRun: time.Now().Format(time.RFC3339), Status: "failed", Error: fmt.Sprintf("panic: %v", recovered), NextRun: time.Now().Add(5 * time.Second).Format(time.RFC3339)})
							s.log.Printf("wa-bridge panic: %v", recovered)
						}
					}()
					s.superviseWA(ctx, bridgePath)
				}()
				if !panicked || !wait(ctx, 5*time.Second) {
					return
				}
			}
		}()
	}
	<-ctx.Done()
	s.wg.Wait()
}

func (s *Service) startFederation(ctx context.Context) {
	if s.config.Fed == nil {
		return
	}
	switch s.config.Fed.Mode {
	case "hub":
		hub, err := fed.NewHub(s.config.StateDir, s.config.Fed.PeerName, s.queue)
		if err != nil {
			s.setState("fed-server", JobState{LastRun: time.Now().Format(time.RFC3339), Status: "failed", Error: err.Error()})
			s.log.Printf("fed-server: %v", err)
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			started := time.Now()
			s.setState("fed-server", JobState{LastRun: started.Format(time.RFC3339), Status: "running"})
			if err := hub.Serve(ctx, s.config.Fed.Listen); err != nil && ctx.Err() == nil {
				s.setState("fed-server", JobState{LastRun: started.Format(time.RFC3339), Status: "failed", Error: err.Error(), Duration: time.Since(started).Round(time.Millisecond).String()})
				s.log.Printf("fed-server: %v", err)
				return
			}
			s.setState("fed-server", JobState{LastRun: started.Format(time.RFC3339), Status: "stopped", Duration: time.Since(started).Round(time.Millisecond).String()})
		}()
	case "client":
		client := fed.NewClient(s.config.Fed.Hub, s.config.Fed.Token)
		failures := 0
		reported := false
		s.startLoop(ctx, "fed-poll", 5*time.Second, 5*time.Second, func(run context.Context, interval time.Duration) {
			started := time.Now()
			count, err := client.PollAndEnqueue(run, s.config.StateDir, s.queue)
			state := JobState{LastRun: started.Format(time.RFC3339), Status: "ok", NextRun: started.Add(interval).Format(time.RFC3339), Duration: time.Since(started).Round(time.Millisecond).String()}
			if err != nil {
				failures++
				state.Status = "failed"
				state.Error = err.Error()
				if failures >= 5 && !reported {
					s.log.Printf("fed-poll: %d consecutive failures: %v", failures, err)
					reported = true
				}
			} else {
				if count > 0 {
					s.log.Printf("fed-poll: queued %d message(s)", count)
				}
				failures = 0
				reported = false
			}
			if run.Err() != nil {
				state.Status = "stopped"
				state.Error = ""
			}
			s.setState("fed-poll", state)
		})
	}
}

func (s *Service) usageBinary(name string) (string, bool) {
	if s.config.UsageBin != "" {
		path := filepath.Join(s.config.UsageBin, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return path, true
		}
	}
	s.log.Printf("skipping %s: not configured", name)
	return "", false
}

// startLoop uses a buffered work channel and one worker, so ticks never overlap.
func (s *Service) startLoop(ctx context.Context, name string, initialDelay, interval time.Duration, work func(context.Context, time.Duration)) {
	trigger := make(chan struct{}, 1)
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		if !wait(ctx, initialDelay) {
			return
		}
		trigger <- struct{}{}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case trigger <- struct{}{}:
				default:
				}
			}
		}
	}()
	go func() {
		defer s.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-trigger:
				func() {
					defer func() {
						if recovered := recover(); recovered != nil {
							s.setState(name, JobState{LastRun: time.Now().Format(time.RFC3339), Status: "failed", Error: fmt.Sprintf("panic: %v", recovered), NextRun: time.Now().Add(interval).Format(time.RFC3339)})
							s.log.Printf("%s panic: %v", name, recovered)
						}
					}()
					work(ctx, interval)
				}()
			}
		}
	}()
}

func (s *Service) tracked(ctx context.Context, name string, interval time.Duration, work func() error) error {
	started := time.Now()
	next := started.Add(interval).Format(time.RFC3339)
	s.setState(name, JobState{LastRun: started.Format(time.RFC3339), Status: "running", NextRun: next})
	err := work()
	state := JobState{LastRun: started.Format(time.RFC3339), Status: "ok", NextRun: next, Duration: time.Since(started).Round(time.Millisecond).String()}
	if err != nil {
		state.Status = "failed"
		state.Error = err.Error()
		s.log.Printf("%s: %v", name, err)
	}
	if ctx.Err() != nil {
		state.Status = "stopped"
		state.Error = ""
	}
	s.setState(name, state)
	return err
}

func (s *Service) setState(name string, state JobState) {
	if err := s.state.update(name, state); err != nil {
		s.log.Printf("could not write jobs state (%s): %v", name, err)
	}
}

func command(ctx context.Context, args ...string) error {
	return commandDir(ctx, "", args...)
}

func commandDir(ctx context.Context, dir string, args ...string) error {
	return commandDirEnv(ctx, dir, nil, args...)
}

func commandDirEnv(ctx context.Context, dir string, extraEnv []string, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "TMUX_TMPDIR=/tmp")
	cmd.Env = append(cmd.Env, extraEnv...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", args[0], err)
	}
	return nil
}

func (s *Service) keepalive(ctx context.Context) error {
	if s.tmux.HasSession(ctx, "server-main") {
		return nil
	}
	s.log.Print("server-main tmux is missing; opening it")
	dir := s.config.Home
	if s.config.Legacy {
		dir = "/srv"
	}
	if err := s.tmux.Open(ctx, "server-main", dir, bptmux.OpenOptions{Resume: true, NoPrompt: true, Legacy: s.config.Legacy}, func(message string) { s.log.Print(message) }); err != nil {
		return err
	}
	return book.SetStatus(s.config.Agentbooks, "server-main", "open", dir)
}

func (s *Service) superviseWA(ctx context.Context, bridgePath string) {
	const name = "wa-bridge"
	for ctx.Err() == nil {
		started := time.Now()
		cmd := exec.Command("node", bridgePath)
		cmd.Dir = filepath.Dir(bridgePath)
		cmd.Env = append(os.Environ(), "TMUX_TMPDIR=/tmp")
		cmd.Stdout = io.Discard
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			s.setState(name, JobState{LastRun: started.Format(time.RFC3339), Status: "failed", Error: err.Error(), NextRun: time.Now().Add(5 * time.Second).Format(time.RFC3339)})
			if !wait(ctx, 5*time.Second) {
				return
			}
			continue
		}
		s.setState(name, JobState{LastRun: started.Format(time.RFC3339), Status: "running"})
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if ctx.Err() != nil {
				s.setState(name, JobState{LastRun: started.Format(time.RFC3339), Status: "stopped", Duration: time.Since(started).Round(time.Millisecond).String()})
				return
			}
			state := JobState{LastRun: started.Format(time.RFC3339), Status: "failed", Duration: time.Since(started).Round(time.Millisecond).String(), NextRun: time.Now().Add(5 * time.Second).Format(time.RFC3339)}
			if err != nil {
				state.Error = err.Error()
			} else {
				state.Error = "unexpected exit"
			}
			s.setState(name, state)
			if !wait(ctx, 5*time.Second) {
				return
			}
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
			s.setState(name, JobState{LastRun: started.Format(time.RFC3339), Status: "stopped", Duration: time.Since(started).Round(time.Millisecond).String()})
			return
		}
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
