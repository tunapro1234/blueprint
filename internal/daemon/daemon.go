package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/msgq"
	bptmux "blueprint/internal/tmux"
)

type Service struct {
	state *State
	tmux  *bptmux.Client
	queue *msgq.Queue
	log   *log.Logger
	wg    sync.WaitGroup
}

func New(logger *log.Logger) *Service {
	if logger == nil {
		logger = log.New(os.Stderr, "blueprint: ", log.LstdFlags)
	}
	return &Service{state: NewState(StatePath), tmux: bptmux.New(), queue: msgq.New(msgq.DefaultRoot), log: logger}
}

func (s *Service) Run(ctx context.Context) {
	s.startLoop(ctx, "msgq", 30*time.Second, func(run context.Context, interval time.Duration) {
		s.tracked(run, "msgq", interval, func() error {
			return s.queue.Dispatch(run, s.tmux, func(message string) { s.log.Print(message) })
		})
	})
	s.startLoop(ctx, "keepalive", 2*time.Minute, func(run context.Context, interval time.Duration) {
		s.tracked(run, "keepalive", interval, func() error { return s.keepalive(run) })
	})
	s.startLoop(ctx, "usage-pulse-chain", 10*time.Minute, func(run context.Context, interval time.Duration) {
		steps := []struct {
			name string
			args []string
		}{
			{"usage-pulse", []string{"/srv/server-main/bin/usage-pulse"}},
			{"usage-policy", []string{"/srv/server-main/bin/usage-policy"}},
			{"dashboard-gen", []string{"/usr/bin/python3", "/srv/monitor/site/gen.py"}},
		}
		for _, step := range steps {
			if run.Err() != nil {
				return
			}
			if err := s.tracked(run, step.name, interval, func() error { return command(run, step.args...) }); err != nil {
				return
			}
		}
	})
	s.startLoop(ctx, "usage-watch", 10*time.Minute, func(run context.Context, interval time.Duration) {
		s.tracked(run, "usage-watch", interval, func() error { return command(run, "/srv/server-main/bin/usage-watch") })
	})
	s.startLoop(ctx, "watch-radar-chain", 30*time.Minute, func(run context.Context, interval time.Duration) {
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
				return commandDirEnv(run, "/srv/monitor/watch", []string{"AGENT=server-monitor-dash"}, step.args...)
			}); err != nil {
				return
			}
		}
	})
	s.startLoop(ctx, "watch-reset", 10*time.Minute, func(run context.Context, interval time.Duration) {
		s.tracked(run, "watch-reset", interval, func() error {
			return commandDirEnv(run, "/srv/monitor/watch", []string{"AGENT=server-monitor-dash"}, "/usr/bin/python3", "/srv/monitor/watch/reset_watch.py")
		})
	})
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
				s.superviseWA(ctx)
			}()
			if !panicked || !wait(ctx, 5*time.Second) {
				return
			}
		}
	}()
	<-ctx.Done()
	s.wg.Wait()
}

// startLoop uses a buffered work channel and one worker, so ticks never overlap.
func (s *Service) startLoop(ctx context.Context, name string, interval time.Duration, work func(context.Context, time.Duration)) {
	trigger := make(chan struct{}, 1)
	trigger <- struct{}{}
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
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
		s.log.Printf("jobs state yazilamadi (%s): %v", name, err)
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
		return fmt.Errorf("bos komut")
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "HOME=/root", "TMUX_TMPDIR=/tmp")
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
	s.log.Print("server-main tmux YOK — aciliyor")
	if err := s.tmux.Open(ctx, "server-main", "/srv", bptmux.OpenOptions{Resume: true, NoPrompt: true}, func(message string) { s.log.Print(message) }); err != nil {
		return err
	}
	return book.SetStatus("server-main", "open", "/srv")
}

func (s *Service) superviseWA(ctx context.Context) {
	const name = "wa-bridge"
	for ctx.Err() == nil {
		started := time.Now()
		cmd := exec.Command("/usr/bin/node", "/srv/whatsapp/bridge.js")
		cmd.Dir = "/srv/whatsapp"
		cmd.Env = append(os.Environ(), "HOME=/root", "TMUX_TMPDIR=/tmp")
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
				state.Error = "beklenmedik cikis"
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
