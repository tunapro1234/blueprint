package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
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
	queue := msgq.New(cfg.MsgqRoot)
	// Reconciliation: before the queue re-pastes anything, it asks the target's own
	// transcript whether the message already arrived. Without this, a message that
	// landed some other way (hand-delivered, or submitted out of a composer where
	// it had been hanging) is pasted a second time.
	queue.Witness = book.DeliveryWitness(cfg.Agentbooks, bptmux.ClaudeProjectsRoot())
	// And the other half of the same question: whether a given text is one the
	// witness could ever recognise. The queue holds an unconfirmed delivery open
	// for the transcript only when the answer is yes — otherwise nothing would
	// ever settle the record.
	queue.CanWitness = book.CanWitness
	// The busy question, asked of the transcript rather than the screen. The pane
	// draws nothing at all while a long answer streams, so the screen gate alone
	// lets the queue paste into a working agent; this is what closes that window.
	queue.TurnOpen = book.TurnOpenProbe(cfg.Agentbooks, bptmux.ClaudeProjectsRoot())
	return &Service{config: cfg, state: NewState(filepath.Join(cfg.StateDir, "jobs.json")), tmux: bptmux.New(), queue: queue, log: logger}
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
			config := tokens.DefaultConfig(s.config.StateDir, s.config.TokenAgentbooks)
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
	s.startLoop(ctx, "busy-sanity", 5*time.Minute, time.Hour, func(run context.Context, interval time.Duration) {
		s.tracked(run, "busy-sanity", interval, func() error { return s.busySanity(run) })
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
	if s.config.InvalidConfig != "" {
		err := fmt.Errorf("federation disabled because config.json is invalid: %s", s.config.InvalidConfig)
		s.setState("fed-server", JobState{LastRun: time.Now().Format(time.RFC3339), Status: "failed", Error: err.Error()})
		s.log.Printf("fed-server: %v", err)
		return
	}
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
		hub.Log = s.log.Writer()
		hub.Outbox.Log = s.log.Writer()
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
		client.Log = s.log.Writer()
		client.Expose = s.config.Fed.Expose
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
	return book.SetStatus(s.config.Agentbooks, "server-main", "open", dir, book.Registration{})
}

// --- busy-sanity: a watchdog for the busy DETECTOR, not for the agents --------
//
// On 2026-08-15 Claude Code 2.1.233 stopped printing "esc to interrupt" in a
// working pane, and tmux.Busy — which required that phrase before looking at
// anything else — began returning false for every Claude pane on the machine.
// Nothing failed loudly: `bp status` showed an idle fleet, and every guard built
// on Busy (compact, the queue's refusal to type into a working pane, the send
// path's pre-flight check) quietly stopped guarding. The signature had drifted
// once before, and it will drift again; what was missing was not a better regex
// but a way to NOTICE.
//
// So this loop watches the detector against a fact that no UI change can take
// away: panes whose content keeps changing are panes where turns are running. If
// agent panes have been moving for a whole day while Busy never once said "yes",
// the detector — not the fleet — is what stopped working, and the operator hears
// about it in one message rather than after weeks of paper guarantees.
//
// Deliberately dull about everything else: it presses nothing, touches no pane,
// and its verdict is one message per day at most.
//
// WHAT IT MEASURES, and why that changed on 2026-08-18. The first version judged
// Busy against pane CONTENT CHANGE: "agent panes moved for a whole day while Busy
// never said yes" raised the alarm. That rule fired on 2026-08-18 with the screen
// signature perfectly intact — a live capture that day read
// "* Zigzagging… (37s · ↓ 1.3k tokens)" (plain ASCII star, a two-line Tip block
// wedged between it and the composer) and Busy returned true on it inside the
// 8-row window. Three structural reasons, none of them drift:
//
//   - an hourly sweep is POINT sampling. Short turns on a quiet night are simply
//     not there at the second the sweep looks, so LastBusySeen ages out;
//   - the activity side has no such problem — hashes differ if the pane moved at
//     ANY point BETWEEN two sweeps, so it keeps filling from turns the busy side
//     could never have caught. The two observations are not sampled alike, and
//     comparing them compares sampling rates as much as detectors;
//   - the screen is blind for a whole phase by design (a streaming answer draws no
//     indicator at all — see book.TurnOpen), so a stream-heavy day makes an
//     entirely healthy screen gate look mute.
//
// So "activity without busy" is structurally open to false alarms, and a watchdog
// that cries wolf is one nobody reads. What replaces it is CROSS-GATE COHERENCE.
// There are two independent busy gates — the screen (bptmux.Busy) and the
// transcript (book.TurnOpen) — and in a healthy fleet they largely overlap. Every
// sweep in which some pane reads TurnOpen=true is a sample of "this agent is
// mid-turn", i.e. evidence that the SCREEN should have been showing something too
// at some point; the loop counts those samples and waits for a sweep in which one
// pane satisfies BOTH gates at once. When that agreement stops happening while the
// samples pile up, the screen signature has drifted — and this is provable in
// hours rather than assumed from a day of silence.
//
// The counter is what makes the two failure modes separable. A genuinely quiet
// fleet accumulates no TurnOpen samples at all, so there is no evidence, and no
// alarm: exactly the false-alarm class above, gone. The streaming blind window
// explains single samples but cannot explain twelve — tool and thinking phases are
// the bulk of every turn and both draw the spinner, so twelve independent mid-turn
// samples without one agreement is a signature change, not a statistic.
//
// The screen gate remains the detector UNDER TEST: TurnOpen is read only as the
// witness that work existed, never as an answer to "is Busy working". LastBusySeen
// and LastActivitySeen are still recorded because they are worth having in hand
// during an incident, but nothing alarms on them any more.
const (
	// busySanityFile lives under StateDir next to jobs.json.
	busySanityFile = "busy-sanity.json"
	// busySanityWindow is the alarm's cooldown: say it once a day, not hourly.
	busySanityWindow = 24 * time.Hour
	// busySanityGateSamples is how much mid-turn evidence must go unmatched by the
	// screen before the loop calls it drift. Twelve is chosen against the one thing
	// that innocently produces unmatched samples — the streaming blind window —
	// where a single sample is unremarkable and twelve in a row is not: streaming is
	// a minority of a turn's wall clock, and these samples come from different
	// sweeps and usually different panes.
	busySanityGateSamples = 12
	// busySanityGateSweepCap is the most one sweep may contribute to that count, and
	// it is what makes the samples INDEPENDENT rather than merely numerous. A sweep
	// reads every pane in one instant, so a twenty-agent fleet could in principle
	// hand over twelve samples in a single frame — and a single frame is exactly what
	// the streaming blind window can cover. A quarter of the threshold means at least
	// four sweeps must speak before the count is full. It costs nothing in practice
	// (a healthy hourly loop cannot reach twelve inside the silence clause anyway,
	// and a drifted fleet keeps producing samples every hour) and it covers the two
	// cases where sweeps are not an hour apart: repeated daemon restarts, and the
	// first sweeps after this measure ships onto a state file that already carries an
	// old observation start and no agreement.
	busySanityGateSweepCap = busySanityGateSamples / 4
	// busySanityGateSilence is how long the agreement must have been missing on top
	// of that. It is the second, independent axis: the counter can fill quickly on a
	// busy fleet, and this keeps a burst of samples inside one working hour from
	// standing in for a lasting divergence. It also covers the fresh install, where
	// there is no agreement yet and the observation start is measured from instead.
	busySanityGateSilence = 6 * time.Hour
)

// busySanityMessage is written for a human, so it names the suspicion, the thing
// that is now untrustworthy, where to look — and, just as important during an
// incident, what still works: the queue and `bp status` OR both gates, so the
// transcript gate is holding the fleet's busy verdicts up while the screen one is
// suspect.
func busySanityMessage(samples int) string {
	return fmt.Sprintf("bp: ekran mesguliyet imzasi ile transcript gecidi %d ornektir ortusmuyor — ekran imzasi kaymis olabilir (Claude Code guncellemesi?); internal/tmux Busy() imzalarini kontrol et. bp status guvenilir (TurnOpen calisiyor).", samples)
}

// busySanityState is the whole memory of the loop. Timestamps are RFC3339 so the
// file can be read by a human during an incident.
type busySanityState struct {
	// Since is when observation started (first run, or the run after an
	// unreadable file). It stands in for LastGateAgree until the two gates have
	// agreed once, so a fresh install cannot alarm out of its first hour.
	Since string `json:"since,omitempty"`
	// TurnOpenSamples counts, since the last screen agreement, the panes seen
	// mid-turn by the transcript gate. It is one running total for the fleet
	// rather than a tally per pane: what is being counted is evidence that the
	// screen had something to show, and any pane's turn is that evidence.
	TurnOpenSamples int `json:"turn_open_samples,omitempty"`
	// LastGateAgree: the last time ONE pane satisfied BOTH gates in ONE sweep.
	// This is the fact whose absence is the alarm, and seeing it resets the count.
	LastGateAgree string `json:"last_gate_agree,omitempty"`
	// LastBusySeen: the last time ANY agent pane read as busy. Diagnostic only
	// since 2026-08-18 — kept because it is the first thing worth knowing when the
	// alarm does fire, but no longer able to raise one.
	LastBusySeen string `json:"last_busy_seen,omitempty"`
	// LastActivitySeen: the last time an agent pane's content DIFFERED from the
	// previous sweep. Also diagnostic only: it is sampled between sweeps while the
	// busy side is sampled at them, which is what made the old rule fire on a
	// healthy detector.
	LastActivitySeen string `json:"last_activity_seen,omitempty"`
	// LastAlarm enforces the one-per-window cooldown.
	LastAlarm string `json:"last_alarm,omitempty"`
	// PaneHashes carries the previous sweep's fingerprints, per session.
	PaneHashes map[string]string `json:"pane_hashes,omitempty"`
	// MergeSeen holds the "<agent>@<record timestamp>" keys of the damaged
	// deliveries already reported (see merge.go). It shares this file because it
	// shares the loop, and because both are answers to the same question: is the
	// machinery under bp still doing what it says.
	MergeSeen []string `json:"merge_seen,omitempty"`
	// MergeWatermark is the moment up to which transcripts have already been
	// examined for damaged deliveries. Only records NEWER than it are ever
	// reported, so the detector's silence means "clean since the last sweep" —
	// not "clean, or drowning you in history". See merge.go for why it exists.
	MergeWatermark string `json:"merge_watermark,omitempty"`
}

// busySanity runs one sweep. Errors from a single pane are never fatal: a session
// that closed between the listing and the capture proves nothing either way, and
// the loop must keep its own state file current regardless.
func (s *Service) busySanity(ctx context.Context) error {
	path := filepath.Join(s.config.StateDir, busySanityFile)
	state := readBusySanity(path)
	sessions, err := s.tmux.Sessions(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	// The transcript gate needs each agent's folder, and the fleet changes under a
	// long-running daemon, so the agentbooks are re-read every sweep — the same
	// reason book.TurnOpenProbe re-reads them per call. TurnOpen is called directly
	// instead of through the probe so one load serves the whole sweep. An
	// unreadable book leaves an empty fleet, which resolves no folder, which yields
	// no samples: the loop goes quiet rather than guessing, which is the only safe
	// direction for a watchdog.
	fleet, _ := book.LoadFleet(book.Paths(s.config.Agentbooks))
	projects := bptmux.ClaudeProjectsRoot()
	hashes := make(map[string]string, len(sessions))
	busy, moved, agreed := false, false, false
	samples := 0
	for _, session := range sessions {
		// Only agent panes count, on every signal. A plain shell tailing a log
		// changes every second and can never be busy, and letting it feed the
		// activity side would eventually raise an alarm about nothing — which is
		// the one way a watchdog like this gets ignored.
		process, err := s.tmux.PaneProcess(ctx, session)
		if err != nil {
			continue
		}
		pane, err := s.tmux.Capture(ctx, session)
		if err != nil {
			continue
		}
		// The capture is taken BEFORE the agent test rather than after it, because
		// the test now needs it: a Hermes pane reports "python" and is only an agent
		// pane when its screen says so (2026-08-22). The order change costs one
		// capture per non-agent session in a sweep that already captures every agent
		// one, and buys the watchdog its Hermes samples — a signature that drifts on
		// a pane type nobody is sampling is a signature nobody finds out about.
		if !bptmux.IsAgentPane(process.Command, pane) {
			continue
		}
		hash := paneHash(pane)
		hashes[session] = hash
		screen := bptmux.Busy(pane)
		if screen {
			busy = true
		}
		// The two gates are read on the SAME pane in the SAME sweep, which is what
		// makes agreement mean anything: a Codex pane (no session file) or an agent
		// missing from the book answers false here and simply contributes nothing
		// in either direction.
		if book.TurnOpen(projects, fleet.Agents[session].Folder, session, now) {
			samples++
			if screen {
				agreed = true
			}
		}
		if previous, seen := state.PaneHashes[session]; seen && previous != hash {
			moved = true
		}
	}
	state.PaneHashes = hashes
	if state.Since == "" {
		state.Since = now.Format(time.RFC3339)
	}
	if busy {
		state.LastBusySeen = now.Format(time.RFC3339)
	}
	if moved {
		state.LastActivitySeen = now.Format(time.RFC3339)
	}
	state.observeGates(now, samples, agreed)
	if state.alarmDue(now) {
		state.LastAlarm = now.Format(time.RFC3339)
		message := busySanityMessage(state.TurnOpenSamples)
		s.log.Print(message)
		if s.queue != nil {
			if _, err := s.queue.Enqueue("server-main", "bp", message); err != nil {
				s.log.Printf("busy-sanity: alarm could not be queued: %v", err)
			}
		}
	}
	// The second watchdog on the same hourly beat, and on the same principle: this
	// one reads what the agents RECEIVED and asks whether it still looks like one
	// message per delivery. It shares the state file, never fails the sweep, and is
	// run last so a failure in it cannot cost the busy verdict its bookkeeping.
	s.mergeScan(sessions, &state, now)
	return writeBusySanity(path, state)
}

// observeGates folds one sweep's readings into the running measure, and is kept
// apart from the sweep for the same reason alarmDue is: it can then be read, and
// tested, without tmux.
//
// One agreement anywhere in the fleet clears the whole count, including samples
// taken from other panes in the same sweep. The question the counter asks is
// whether the screen signature still matches ANY live turn, and one match answers
// it — carrying a remainder forward would only make the next alarm fire early on
// evidence that has already been contradicted.
func (b *busySanityState) observeGates(now time.Time, samples int, agreed bool) {
	if agreed {
		b.LastGateAgree = now.Format(time.RFC3339)
		b.TurnOpenSamples = 0
		return
	}
	if samples > busySanityGateSweepCap {
		samples = busySanityGateSweepCap
	}
	b.TurnOpenSamples += samples
}

// alarmDue is the whole decision, kept apart from the sweep so it can be read —
// and tested — without tmux. Three clauses, all of them required:
//
//   - enough unmatched mid-turn evidence has piled up (there was something for
//     the screen to show, repeatedly, and it never showed it);
//   - the gates have not agreed for busySanityGateSilence — measured from the last
//     agreement, or from the start of observation when there has never been one;
//   - no alarm was raised within the cooldown (say it once a day, not hourly).
//
// The counter alone is not the verdict and neither is the elapsed time: samples
// say the evidence exists, hours say the divergence has lasted. A quiet fleet
// fails the first clause, a busy morning of one drifted hour fails the second.
//
// An unparsable or missing timestamp reads as "cannot tell", and cannot-tell never
// alarms: with no observation start and no agreement on record there is nothing to
// measure the silence against. A stamp in the FUTURE (a clock step) only delays.
func (b busySanityState) alarmDue(now time.Time) bool {
	if b.TurnOpenSamples < busySanityGateSamples || recentStamp(b.LastAlarm, now) {
		return false
	}
	// Since is the fallback rather than a separate clause: before the first
	// agreement it IS the moment from which the gates have been out of step.
	agreed := b.LastGateAgree
	if agreed == "" {
		agreed = b.Since
	}
	when, err := time.Parse(time.RFC3339, agreed)
	if err != nil {
		return false
	}
	return now.Sub(when) >= busySanityGateSilence
}

// recentStamp reports whether an RFC3339 stamp lies within the cooldown window
// before now. A stamp in the FUTURE (a clock step) counts as recent: that
// direction only ever delays an alarm, never invents one.
func recentStamp(value string, now time.Time) bool {
	when, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return false
	}
	return now.Sub(when) < busySanityWindow
}

// paneHash fingerprints a capture. FNV-1a is chosen for being cheap and
// dependency-free: the only question asked of it is "did this screen change",
// where a collision costs one missed sweep and nothing else.
func paneHash(pane string) string {
	sum := fnv.New64a()
	_, _ = sum.Write([]byte(pane))
	return fmt.Sprintf("%016x", sum.Sum64())
}

// readBusySanity returns the previous sweep's state, or a zero state whenever the
// file is missing or unreadable. A zero state restarts the observation window
// rather than alarming on partial evidence.
func readBusySanity(path string) busySanityState {
	data, err := os.ReadFile(path)
	if err != nil {
		return busySanityState{}
	}
	var state busySanityState
	if err := json.Unmarshal(data, &state); err != nil {
		return busySanityState{}
	}
	return state
}

// writeBusySanity replaces the file atomically, so a sweep interrupted mid-write
// leaves the previous state rather than a truncated one.
func writeBusySanity(path string, state busySanityState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0644); err != nil {
		return err
	}
	return os.Rename(temporary, path)
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
