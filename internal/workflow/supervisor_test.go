package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitSupervisor(t *testing.T, supervisor *Supervisor, runID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		supervisor.mu.Lock()
		active := supervisor.active[runID]
		supervisor.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("supervisor run did not finish")
}

func TestSupervisorRestartBackoffAndCap(t *testing.T) {
	t.Run("restarts then completes", func(t *testing.T) {
		clock := newFakeClock()
		store, run := makeRun(t, clock, baseYAML(""), `[{"id":"one"}]`, "{{.Key}}", "", "agent-a")
		attempts := 0
		var delays []time.Duration
		var mu sync.Mutex
		launch := func(_ context.Context, runID string) (ChildWait, error) {
			mu.Lock()
			attempts++
			attempt := attempts
			mu.Unlock()
			return func() error {
				if attempt < 3 {
					return errors.New("unexpected exit")
				}
				_, err := store.UpdateRun(runID, func(current *Run) error { current.Status = "done"; return nil })
				return err
			}, nil
		}
		supervisor := NewSupervisor(store, store.StateDir, launch, nil)
		supervisor.Delay = func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		}
		if err := supervisor.Start(run.ID); err != nil {
			t.Fatal(err)
		}
		waitSupervisor(t, supervisor, run.ID)
		updated, _ := store.LoadRun(run.ID)
		if attempts != 3 || updated.Status != "done" || updated.RestartCount != 2 || len(delays) != 2 || delays[0] != time.Second || delays[1] != 2*time.Second {
			t.Fatalf("attempts=%d run=%#v delays=%v", attempts, updated, delays)
		}
	})
	t.Run("stops after restart cap", func(t *testing.T) {
		clock := newFakeClock()
		store, run := makeRun(t, clock, baseYAML(""), `[{"id":"one"}]`, "{{.Key}}", "", "agent-a")
		attempts := 0
		launch := func(_ context.Context, _ string) (ChildWait, error) {
			attempts++
			return func() error { return fmt.Errorf("exit %d", attempts) }, nil
		}
		supervisor := NewSupervisor(store, store.StateDir, launch, nil)
		supervisor.MaxRestarts = 2
		supervisor.Delay = func(context.Context, time.Duration) error { return nil }
		if err := supervisor.Start(run.ID); err != nil {
			t.Fatal(err)
		}
		waitSupervisor(t, supervisor, run.ID)
		updated, _ := store.LoadRun(run.ID)
		if attempts != 3 || updated.Status != "error" || updated.RestartCount != 2 || !strings.Contains(updated.LastError, "exited unexpectedly") {
			t.Fatalf("restart cap not enforced: attempts=%d run=%#v", attempts, updated)
		}
	})
}

func TestSupervisorResetsRestartCountAfterLongChild(t *testing.T) {
	clock := newFakeClock()
	store, run := makeRun(t, clock, baseYAML(""), `[{"id":"one"}]`, "{{.Key}}", "", "agent-a")
	if _, err := store.UpdateRun(run.ID, func(current *Run) error {
		current.RestartCount = 5
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	launch := func(_ context.Context, id string) (ChildWait, error) {
		attempts++
		attempt := attempts
		return func() error {
			if attempt == 1 {
				clock.Advance(10 * time.Minute)
				return errors.New("late crash")
			}
			_, err := store.UpdateRun(id, func(current *Run) error { current.Status = "done"; return nil })
			return err
		}, nil
	}
	supervisor := NewSupervisor(store, store.StateDir, launch, nil)
	supervisor.MaxRestarts = 6
	supervisor.Now = clock.Now
	supervisor.Delay = func(context.Context, time.Duration) error { return nil }
	if err := supervisor.Start(run.ID); err != nil {
		t.Fatal(err)
	}
	waitSupervisor(t, supervisor, run.ID)
	updated, err := store.LoadRun(run.ID)
	if err != nil || attempts != 2 || updated.Status != "done" || updated.RestartCount != 1 {
		t.Fatalf("long-lived child did not reset its crash budget: attempts=%d run=%#v err=%v", attempts, updated, err)
	}
}

func TestSupervisorAgentLockAndStartupRestore(t *testing.T) {
	clock := newFakeClock()
	store, first := makeRun(t, clock, baseYAML(""), `[{"id":"one"}]`, "{{.Key}}", "", "agent-a")
	source := filepath.Join(filepath.Dir(first.Workdir), "workflow-src")
	definition, err := ReadDefinition(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Add(source, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Workdir, "units.json"), []byte(`[{"id":"two"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateRun(store.WorkflowPath(definition.Name), definition, first.Workdir, []string{"agent-a"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	launch := func(_ context.Context, id string) (ChildWait, error) {
		return func() error {
			started <- struct{}{}
			<-release
			_, err := store.UpdateRun(id, func(current *Run) error { current.Status = "done"; return nil })
			return err
		}, nil
	}
	supervisor := NewSupervisor(store, store.StateDir, launch, nil)
	if err := supervisor.Start(first.ID); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := supervisor.Start(second.ID); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("expected in-process agent lock refusal, got %v", err)
	}
	other := NewSupervisor(store, store.StateDir, launch, nil)
	if err := other.Start(second.ID); err == nil || !strings.Contains(err.Error(), "already locked") {
		t.Fatalf("expected cross-supervisor file lock refusal, got %v", err)
	}
	close(release)
	waitSupervisor(t, supervisor, first.ID)

	// A fresh manager resumes runs persisted as running when its daemon starts.
	if _, err := store.UpdateRun(second.ID, func(current *Run) error { current.Status = "running"; return nil }); err != nil {
		t.Fatal(err)
	}
	var restored int
	restore := NewSupervisor(store, store.StateDir, func(_ context.Context, id string) (ChildWait, error) {
		if id == second.ID {
			restored++
		}
		return func() error {
			_, err := store.UpdateRun(id, func(current *Run) error { current.Status = "done"; return nil })
			return err
		}, nil
	}, nil)
	if err := restore.restore(); err != nil {
		t.Fatal(err)
	}
	waitSupervisor(t, restore, second.ID)
	if restored != 1 {
		t.Fatalf("startup did not restore the active run: %d", restored)
	}
}
