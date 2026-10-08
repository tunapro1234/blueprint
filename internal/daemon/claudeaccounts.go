package daemon

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"time"

	"blueprint/internal/book"
	"blueprint/internal/claudeacct"
	"blueprint/internal/config"
	"blueprint/internal/ntfy"
	bptmux "blueprint/internal/tmux"
)

const (
	claudeAccountJob = "claude-account-auto"
	// claudeAccountTick is the pass interval; each pass polls at most one
	// account, so usage is fetched at most once a minute.
	claudeAccountTick = time.Minute
	// claudeAccountQuietLog bounds how often a persisting condition (every
	// account exhausted, the same error) is logged again.
	claudeAccountQuietLog = time.Hour
)

// claudeAccountAuto runs the bp account auto decision and the keepalive pass
// once per tick. It holds the memory that keeps the daemon log quiet: one
// line per switch and per keepalive ping, one line per hour while every
// account is exhausted or the same error repeats.
type claudeAccountAuto struct {
	manager    *claudeacct.Manager
	options    claudeacct.AutoOptions
	autoSwitch bool
	keepAlive  *claudeacct.KeepAliveOptions
	log        *log.Logger
	notify     func(context.Context, string) error
	now        func() time.Time

	exhaustedAt time.Time
	lastError   string
	errorAt     time.Time
	// keepErrors remembers when each account's keepalive error was logged.
	keepErrors map[int]quietError
}

type quietError struct {
	text string
	at   time.Time
}

func newClaudeAccountAuto(cfg config.Config, logger *log.Logger) *claudeAccountAuto {
	accounts := cfg.ClaudeAccounts
	auto := &claudeAccountAuto{
		manager: &claudeacct.Manager{
			Store:  claudeacct.NewStore(cfg.StateDir),
			Env:    claudeacct.OSEnv(),
			Client: &claudeacct.Client{},
		},
		options: claudeacct.AutoOptions{
			Policy: claudeacct.AutoPolicy{
				Threshold: float64(accounts.Threshold),
				Cooldown:  time.Duration(accounts.CooldownMinutes) * time.Minute,
				Limits:    accounts.LimitPercents(),
			},
			PollEvery: time.Duration(accounts.PollMinutes) * time.Minute,
			MaxPolls:  1,
		},
		autoSwitch: accounts.AutoSwitch,
		log:        logger,
		notify:     func(ctx context.Context, text string) error { return ntfy.Send(ctx, cfg.Ntfy, text) },
		now:        time.Now,
		keepErrors: map[int]quietError{},
	}
	if accounts.KeepAlive {
		auto.keepAlive = &claudeacct.KeepAliveOptions{Model: accounts.KeepAliveModel}
	}
	return auto
}

// startClaudeAccountAuto starts the opt-in auto-switch and keepalive job.
func (s *Service) startClaudeAccountAuto(ctx context.Context) {
	if !s.config.ClaudeAccounts.AutoSwitch && !s.config.ClaudeAccounts.KeepAlive {
		return
	}
	if runtime.GOOS == "darwin" {
		s.log.Printf("%s: disabled on macOS (Claude Code keeps its login in the Keychain)", claudeAccountJob)
		return
	}
	auto := newClaudeAccountAuto(s.config, s.log)
	s.startLoop(ctx, claudeAccountJob, claudeAccountTick, claudeAccountTick, func(run context.Context, interval time.Duration) {
		started := time.Now()
		err := auto.pass(run)
		state := JobState{LastRun: started.Format(time.RFC3339), Status: "ok", NextRun: started.Add(interval).Format(time.RFC3339), Duration: time.Since(started).Round(time.Millisecond).String()}
		if err != nil {
			state.Status, state.Error = "failed", err.Error()
		}
		if run.Err() != nil {
			state.Status, state.Error = "stopped", ""
		}
		s.setState(claudeAccountJob, state)
	})
}

// pass runs one auto check (or, without auto switching, one usage poll) and
// then the keepalive pass.
func (c *claudeAccountAuto) pass(ctx context.Context) error {
	var err error
	if c.autoSwitch {
		err = c.switchPass(ctx)
	} else {
		_, err = c.manager.PollOnce(ctx, c.options.PollEvery, c.options.MaxPolls)
		err = c.quiet(ctx, err)
	}
	if c.keepAlive == nil || ctx.Err() != nil {
		return err
	}
	if keepErr := c.keepAlivePass(ctx); keepErr != nil && err == nil {
		err = keepErr
	}
	return err
}

// quiet logs an error unless the same one was logged within the hour.
func (c *claudeAccountAuto) quiet(ctx context.Context, err error) error {
	if err == nil {
		c.lastError = ""
		return nil
	}
	now := c.now()
	if ctx.Err() == nil && (err.Error() != c.lastError || now.Sub(c.errorAt) >= claudeAccountQuietLog) {
		c.log.Printf("%s: %v", claudeAccountJob, err)
		c.lastError, c.errorAt = err.Error(), now
	}
	return err
}

// keepAlivePass pings the accounts whose staggered window start has come and
// logs each ping and, at most hourly, each account's repeating error.
func (c *claudeAccountAuto) keepAlivePass(ctx context.Context) error {
	result, err := c.manager.KeepAliveOnce(ctx, *c.keepAlive)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Printf("%s: keepalive: %v", claudeAccountJob, err)
		}
		return err
	}
	now := c.now()
	for _, view := range result.Slots {
		name := fmt.Sprintf("slot %d (%s)", view.Slot, view.Email)
		switch {
		case view.Pinged && view.Started:
			c.log.Printf("%s: keepalive started the five-hour window of %s; it resets at %s", claudeAccountJob, name, view.ResetsAt.Local().Format("15:04"))
			delete(c.keepErrors, view.Slot)
		case view.Pinged:
			c.log.Printf("%s: keepalive pinged %s; usage has not confirmed the window yet", claudeAccountJob, name)
		case view.Attempted && view.Error != "":
			last := c.keepErrors[view.Slot]
			if view.Error != last.text || now.Sub(last.at) >= claudeAccountQuietLog {
				c.log.Printf("%s: keepalive for %s failed: %s", claudeAccountJob, name, view.Error)
				c.keepErrors[view.Slot] = quietError{view.Error, now}
			}
		}
	}
	return nil
}

// switchPass runs one auto check and logs/notifies its outcome.
func (c *claudeAccountAuto) switchPass(ctx context.Context) error {
	result, err := c.manager.AutoOnce(ctx, c.options)
	now := c.now()
	if err != nil {
		if ctx.Err() == nil && (err.Error() != c.lastError || now.Sub(c.errorAt) >= claudeAccountQuietLog) {
			c.log.Printf("%s: %v", claudeAccountJob, err)
			c.lastError, c.errorAt = err.Error(), now
		}
		return err
	}
	c.lastError = ""
	d := result.Decision
	switch d.Action {
	case claudeacct.AutoSwitch:
		c.exhaustedAt = time.Time{}
		if result.Switched == nil {
			return nil
		}
		to := result.Switched.To
		message := fmt.Sprintf("Claude account switched from slot %d (%.0f%%) to slot %d (%s, %.0f%%); running Claude agents pick it up on their next request",
			d.From, d.ActiveMax, to.Number, to.Email, d.TargetMax)
		c.log.Printf("%s: %s", claudeAccountJob, message)
		if err := c.notify(ctx, message); err != nil {
			c.log.Printf("%s: notification failed: %v", claudeAccountJob, err)
		}
	case claudeacct.AutoNoTarget:
		if c.exhaustedAt.IsZero() || now.Sub(c.exhaustedAt) >= claudeAccountQuietLog {
			c.log.Printf("%s: no viable Claude account to switch to, staying on slot %d: %s", claudeAccountJob, d.From, d.Reason)
			c.exhaustedAt = now
		}
	default:
		c.exhaustedAt = time.Time{}
	}
	return nil
}

// keepaliveAccount puts the coordinator back on its bound Claude account. A
// binding that cannot be honoured (no profile login yet) is logged and the
// coordinator comes back on the default login: being up matters more than
// which account pays for it.
func (s *Service) keepaliveAccount(fleet book.Fleet, name string, opts *bptmux.OpenOptions) {
	opts.ClaudeAccount, opts.ClaudeConfigDir = "", ""
	if opts.Codex || opts.Hermes || opts.OpenCode {
		return
	}
	binding, _ := fleet.EffectiveClaudeAccount(name)
	if binding == "" || s.config.StateDir == "" {
		return
	}
	manager := &claudeacct.Manager{Store: claudeacct.NewStore(s.config.StateDir), Env: claudeacct.OSEnv()}
	dir, slot, err := manager.LaunchDir(binding)
	if err != nil {
		s.log.Printf("keepalive: %s is bound to Claude account %s but relaunches on the default login: %v", name, binding, err)
		return
	}
	opts.ClaudeAccount, opts.ClaudeConfigDir = slot.Email, dir
}
