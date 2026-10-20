package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	bptmux "blueprint/internal/tmux"
)

const (
	remoteOffDefaultWait = 10 * time.Minute
	remoteOffRetryEvery  = 20 * time.Second
	remoteOffWorkers     = 6
)

// remoteOff turns Remote Control off in Claude sessions (all live ones when no
// name is given) and confirms each on screen. A working agent or one whose
// composer holds text is never interrupted: it is retried every
// remoteOffRetryEvery until --wait runs out, and reported as still busy then.
func (a *app) remoteOff(args []string) error {
	wait := remoteOffDefaultWait
	var names []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--wait":
			if i+1 >= len(args) {
				return fmt.Errorf("usage: bp remote --off [--wait <duration>] [<agent>...]")
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d < 0 {
				return fmt.Errorf("--wait: invalid duration %q", args[i+1])
			}
			wait, i = d, i+1
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("usage: bp remote --off [--wait <duration>] [<agent>...]")
		default:
			names = append(names, arg)
		}
	}
	explicit := len(names) > 0
	if !explicit {
		fleet, states, err := a.fleet()
		if err != nil {
			return err
		}
		for name := range fleet.Agents {
			if state, ok := states[name]; ok && state.Alive {
				names = append(names, name)
			}
		}
		sort.Strings(names)
	}
	commands, err := a.tmux.Commands(a.ctx)
	if err != nil {
		return err
	}
	var targets []string
	skipped := 0
	for _, name := range names {
		if !a.tmux.HasSession(a.ctx, name) {
			if explicit {
				fmt.Fprintf(a.out, "  skip   %-28s no session\n", name)
				skipped++
			}
			continue
		}
		// Only Claude has /remote-control; typing it anywhere else would put
		// text into another CLI or a shell prompt.
		if cmd := commands[name]; cmd != "claude" {
			if explicit {
				fmt.Fprintf(a.out, "  skip   %-28s not a Claude session (%s)\n", name, cmd)
				skipped++
			}
			continue
		}
		targets = append(targets, name)
	}

	var mu sync.Mutex
	report := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(a.out, format, args...)
	}
	off, connectedFirst, failed := 0, 0, 0
	deadline := time.Now().Add(wait)
	pending := targets
	for round := 0; len(pending) > 0; round++ {
		if round > 0 {
			if !time.Now().Add(remoteOffRetryEvery).Before(deadline) {
				break
			}
			select {
			case <-a.ctx.Done():
				return a.ctx.Err()
			case <-time.After(remoteOffRetryEvery):
			}
		}
		var busy []string
		jobs := make(chan string)
		var wg sync.WaitGroup
		for w := 0; w < remoteOffWorkers && w < len(pending); w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for name := range jobs {
					wasOff, err := a.tmux.DisconnectRemoteControl(a.ctx, name)
					mu.Lock()
					switch {
					case errors.Is(err, bptmux.ErrBusy) || errors.Is(err, bptmux.ErrTyping):
						busy = append(busy, name)
					case err != nil:
						failed++
					case wasOff:
						off++
						connectedFirst++
					default:
						off++
					}
					mu.Unlock()
					switch {
					case errors.Is(err, bptmux.ErrBusy) || errors.Is(err, bptmux.ErrTyping):
						if round == 0 {
							report("  wait   %-28s busy; retrying when idle\n", name)
						}
					case err != nil:
						report("  error  %-28s %v\n", name, err)
					case wasOff:
						report("  off    %-28s was not connected (the check connected it; disconnected again)\n", name)
					default:
						report("  off    %-28s disconnected\n", name)
					}
				}
			}()
		}
		for _, name := range pending {
			jobs <- name
		}
		close(jobs)
		wg.Wait()
		sort.Strings(busy)
		pending = busy
	}
	for _, name := range pending {
		fmt.Fprintf(a.out, "  busy   %-28s still working after %s; NOT changed (rerun bp remote --off %s)\n", name, wait, name)
	}
	fmt.Fprintf(a.out, "remote off: %d off (%d were not connected), %d still busy, %d failed, %d skipped\n", off, connectedFirst, len(pending), failed, skipped)
	if failed > 0 || len(pending) > 0 {
		return fmt.Errorf("remote control is not confirmed off in %d session(s)", failed+len(pending))
	}
	return nil
}
