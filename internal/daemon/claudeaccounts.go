package daemon

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"time"

	"blueprint/internal/claudeacct"
	"blueprint/internal/config"
	"blueprint/internal/ntfy"
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

// claudeAccountAuto runs the bp account auto decision once per tick. It holds
// the memory that keeps the daemon log quiet: one line per switch, one line
// per hour while every account is exhausted or the same error repeats.
type claudeAccountAuto struct {
	manager *claudeacct.Manager
	options claudeacct.AutoOptions
	log     *log.Logger
	notify  func(context.Context, string) error
	now     func() time.Time

	exhaustedAt time.Time
	lastError   string
	errorAt     time.Time
}

func newClaudeAccountAuto(cfg config.Config, logger *log.Logger) *claudeAccountAuto {
	accounts := cfg.ClaudeAccounts
	return &claudeAccountAuto{
		manager: &claudeacct.Manager{
			Store:  claudeacct.NewStore(cfg.StateDir),
			Env:    claudeacct.OSEnv(),
			Client: &claudeacct.Client{},
		},
		options: claudeacct.AutoOptions{
			Policy: claudeacct.AutoPolicy{
				Threshold: float64(accounts.Threshold),
				Cooldown:  time.Duration(accounts.CooldownMinutes) * time.Minute,
			},
			PollEvery: time.Duration(accounts.PollMinutes) * time.Minute,
			MaxPolls:  1,
		},
		log:    logger,
		notify: func(ctx context.Context, text string) error { return ntfy.Send(ctx, cfg.Ntfy, text) },
		now:    time.Now,
	}
}

// startClaudeAccountAuto starts the opt-in auto-switch job.
func (s *Service) startClaudeAccountAuto(ctx context.Context) {
	if !s.config.ClaudeAccounts.AutoSwitch {
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

// pass runs one auto check and logs/notifies its outcome.
func (c *claudeAccountAuto) pass(ctx context.Context) error {
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
