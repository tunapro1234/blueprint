package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"blueprint/internal/claudeacct"
	bpconfig "blueprint/internal/config"
)

const accountUsage = `usage:
  bp account add [--slot N] [--alias A]
  bp account list [--json] [--refresh]
  bp account status [--json]
  bp account switch [N|email|alias] [--strategy best|next-available] [--dry-run]
  bp account remove N
  bp account alias N A | bp account alias N --unset
  bp account disable N | bp account enable N
  bp account auto [--once] [--dry-run] [--threshold P] [--json]`

// Exit codes of bp account auto --once.
const (
	accountAutoSwitched = 0
	accountAutoNothing  = 2
	accountAutoNoTarget = 3
)

// accountAutoTick is how often the foreground bp account auto loop runs one
// pass; each pass polls at most one account.
const accountAutoTick = time.Minute

// accountGOOS is the platform guard; tests override it.
var accountGOOS = runtime.GOOS

// newAccountManager builds the manager for the live machine. Tests replace
// it to inject a temporary Claude home and fake OAuth endpoints.
var newAccountManager = func(config bpconfig.Config) *claudeacct.Manager {
	return &claudeacct.Manager{
		Store:  claudeacct.NewStore(config.StateDir),
		Env:    claudeacct.OSEnv(),
		Client: &claudeacct.Client{},
	}
}

func (a *app) account(args []string) error {
	if accountGOOS == "darwin" {
		return errors.New("bp account is not supported on macOS: Claude Code keeps its login in the Keychain there, not in a credentials file")
	}
	if len(args) == 0 {
		return errors.New(accountUsage)
	}
	if a.config.StateDir == "" {
		return errors.New("bp account needs a state directory (stateDir in the bp config)")
	}
	manager := newAccountManager(a.config)
	command, rest := args[0], args[1:]
	switch command {
	case "add":
		return a.accountAdd(manager, rest)
	case "list", "ls":
		return a.accountList(manager, rest)
	case "status":
		return a.accountStatus(manager, rest)
	case "switch":
		return a.accountSwitch(manager, rest)
	case "remove", "rm":
		return a.accountRemove(manager, rest)
	case "alias":
		return a.accountAlias(manager, rest)
	case "disable", "enable":
		return a.accountSetDisabled(manager, command == "disable", rest)
	case "auto":
		return a.accountAuto(manager, rest)
	}
	return fmt.Errorf("unknown account command %q\n%s", command, accountUsage)
}

// accountFlags parses the small flag sets of bp account. Flags listed in
// valued take one value (as "--flag v" or "--flag=v"); positional arguments
// are returned in order.
func accountFlags(args []string, valued map[string]bool, boolean map[string]bool) (map[string]string, []string, error) {
	flags := map[string]string{}
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if _, seen := flags[name]; seen {
			return nil, nil, fmt.Errorf("%s given twice\n%s", name, accountUsage)
		}
		switch {
		case valued[name]:
			if !hasValue {
				if i+1 >= len(args) {
					return nil, nil, fmt.Errorf("%s needs a value\n%s", name, accountUsage)
				}
				i++
				value = args[i]
			}
			flags[name] = value
		case boolean[name] && !hasValue:
			flags[name] = "true"
		default:
			return nil, nil, fmt.Errorf("unknown option %s\n%s", arg, accountUsage)
		}
	}
	return flags, positional, nil
}

func slotNumber(text string) (int, error) {
	n, err := strconv.Atoi(text)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid slot %q: expected a positive number", text)
	}
	return n, nil
}

func (a *app) accountJSON(value any) error {
	encoder := json.NewEncoder(a.out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (a *app) accountAdd(m *claudeacct.Manager, args []string) error {
	flags, positional, err := accountFlags(args, map[string]bool{"--slot": true, "--alias": true}, nil)
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		return errors.New(accountUsage)
	}
	number := 0
	if text, ok := flags["--slot"]; ok {
		if number, err = slotNumber(text); err != nil {
			return err
		}
	}
	result, err := m.Add(a.ctx, number, flags["--alias"])
	if err != nil {
		return err
	}
	verb := "stored"
	if result.Updated {
		verb = "updated"
	}
	fmt.Fprintf(a.out, "%s the live Claude login as slot %d (%s)\n", verb, result.Slot.Number, result.Slot.Email)
	return nil
}

func (a *app) accountList(m *claudeacct.Manager, args []string) error {
	flags, positional, err := accountFlags(args, nil, map[string]bool{"--json": true, "--refresh": true})
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		return errors.New(accountUsage)
	}
	overview, err := m.Overview(a.ctx, claudeacct.OverviewOptions{Fetch: true, Force: flags["--refresh"] != "", MaxAge: claudeacct.ListMaxAge})
	if err != nil {
		return err
	}
	if flags["--json"] != "" {
		return a.accountJSON(overview)
	}
	claudeacct.RenderList(a.out, overview)
	return nil
}

func (a *app) accountStatus(m *claudeacct.Manager, args []string) error {
	flags, positional, err := accountFlags(args, nil, map[string]bool{"--json": true})
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		return errors.New(accountUsage)
	}
	// status is a cheap local read: cached usage only, no network.
	overview, err := m.Overview(a.ctx, claudeacct.OverviewOptions{})
	if err != nil {
		return err
	}
	state, err := m.Store.LoadAutoState()
	if err != nil {
		return err
	}
	cfg := a.config.ClaudeAccounts
	if flags["--json"] != "" {
		return a.accountJSON(struct {
			*claudeacct.Overview
			Auto       claudeacct.AutoState          `json:"autoState"`
			AutoConfig bpconfig.ClaudeAccountsConfig `json:"autoConfig"`
		}{overview, state, cfg})
	}
	claudeacct.RenderStatus(a.out, overview, state)
	mode := "off"
	if cfg.AutoSwitch {
		mode = "on"
	}
	fmt.Fprintf(a.out, "auto switch: %s (threshold %d%%, cooldown %dm, poll every %dm)\n", mode, cfg.Threshold, cfg.CooldownMinutes, cfg.PollMinutes)
	return nil
}

func (a *app) accountCooldown() time.Duration {
	return time.Duration(a.config.ClaudeAccounts.CooldownMinutes) * time.Minute
}

func (a *app) accountSwitch(m *claudeacct.Manager, args []string) error {
	flags, positional, err := accountFlags(args, map[string]bool{"--strategy": true}, map[string]bool{"--dry-run": true})
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return errors.New(accountUsage)
	}
	opts := claudeacct.SwitchOptions{Strategy: flags["--strategy"], DryRun: flags["--dry-run"] != "", Cooldown: a.accountCooldown()}
	if _, given := flags["--strategy"]; given && opts.Strategy == "" {
		return errors.New("--strategy needs best or next-available")
	}
	if len(positional) == 1 {
		opts.Selector = positional[0]
	}
	result, err := m.Switch(a.ctx, opts)
	if err != nil {
		return err
	}
	if result.DryRun {
		from := "the live login"
		if result.From != 0 {
			from = fmt.Sprintf("slot %d", result.From)
		}
		fmt.Fprintf(a.out, "would switch from %s to slot %d (%s); nothing changed\n", from, result.To.Number, result.To.Email)
		return nil
	}
	a.printSwitchNotes(result)
	fmt.Fprintf(a.out, "switched to slot %d (%s); running Claude agents pick it up on their next request\n", result.To.Number, result.To.Email)
	return nil
}

func (a *app) printSwitchNotes(result claudeacct.SwitchResult) {
	if result.Captured && result.From != 0 {
		fmt.Fprintf(a.out, "saved the current login back into slot %d\n", result.From)
	}
	if result.Displaced != "" {
		fmt.Fprintf(a.out, "the previous live login was not a stored account; kept a copy at %s\n", result.Displaced)
	}
}

func (a *app) accountRemove(m *claudeacct.Manager, args []string) error {
	if len(args) != 1 {
		return errors.New(accountUsage)
	}
	n, err := slotNumber(args[0])
	if err != nil {
		return err
	}
	slot, err := m.Remove(a.ctx, n)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "removed slot %d (%s); its files were moved under %s/removed\n", slot.Number, slot.Email, m.Store.Root())
	return nil
}

func (a *app) accountAlias(m *claudeacct.Manager, args []string) error {
	if len(args) != 2 {
		return errors.New(accountUsage)
	}
	n, err := slotNumber(args[0])
	if err != nil {
		return err
	}
	alias := args[1]
	if alias == "--unset" {
		alias = ""
	} else if strings.HasPrefix(alias, "-") {
		return fmt.Errorf("unknown option %s\n%s", alias, accountUsage)
	}
	slot, err := m.SetAlias(a.ctx, n, alias)
	if err != nil {
		return err
	}
	if slot.Alias == "" {
		fmt.Fprintf(a.out, "slot %d (%s) has no alias\n", slot.Number, slot.Email)
	} else {
		fmt.Fprintf(a.out, "slot %d (%s) is now %q\n", slot.Number, slot.Email, slot.Alias)
	}
	return nil
}

func (a *app) accountSetDisabled(m *claudeacct.Manager, disabled bool, args []string) error {
	if len(args) != 1 {
		return errors.New(accountUsage)
	}
	n, err := slotNumber(args[0])
	if err != nil {
		return err
	}
	slot, err := m.SetDisabled(a.ctx, n, disabled)
	if err != nil {
		return err
	}
	state := "enabled"
	if disabled {
		state = "disabled; switching and auto switching skip it"
	}
	fmt.Fprintf(a.out, "slot %d (%s) %s\n", slot.Number, slot.Email, state)
	return nil
}

func (a *app) accountAuto(m *claudeacct.Manager, args []string) error {
	flags, positional, err := accountFlags(args, map[string]bool{"--threshold": true}, map[string]bool{"--once": true, "--dry-run": true, "--json": true})
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		return errors.New(accountUsage)
	}
	cfg := a.config.ClaudeAccounts
	policy := claudeacct.AutoPolicy{Threshold: float64(cfg.Threshold), Cooldown: a.accountCooldown()}
	if text, ok := flags["--threshold"]; ok {
		value, err := strconv.ParseFloat(strings.TrimSuffix(text, "%"), 64)
		if err != nil || value <= 0 || value > 100 {
			return fmt.Errorf("invalid --threshold %q: expected a percentage in (0, 100]", text)
		}
		policy.Threshold = value
	}
	opts := claudeacct.AutoOptions{Policy: policy, DryRun: flags["--dry-run"] != "", PollEvery: time.Duration(cfg.PollMinutes) * time.Minute}
	jsonOutput := flags["--json"] != ""
	if flags["--once"] != "" {
		result, err := m.AutoOnce(a.ctx, opts)
		if err != nil {
			return err
		}
		if jsonOutput {
			if err := a.accountJSON(result); err != nil {
				return err
			}
		} else {
			a.printAutoResult(result, opts.DryRun)
		}
		switch result.Decision.Action {
		case claudeacct.AutoSwitch:
			return nil
		case claudeacct.AutoNoTarget:
			return &commandExitError{code: accountAutoNoTarget}
		default:
			return &commandExitError{code: accountAutoNothing}
		}
	}
	// Foreground loop: one pass per minute, polling at most one account per
	// pass, the same cadence as the daemon job.
	opts.MaxPolls = 1
	ctx, stop := signal.NotifyContext(a.ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(a.err, "auto switching at %.0f%% every %s; Ctrl-C stops\n", policy.Threshold, accountAutoTick)
	ticker := time.NewTicker(accountAutoTick)
	defer ticker.Stop()
	for {
		result, err := m.AutoOnce(ctx, opts)
		stamp := time.Now().Format("15:04:05")
		switch {
		case err != nil:
			fmt.Fprintf(a.err, "%s error: %v\n", stamp, err)
		case jsonOutput:
			_ = a.accountJSON(result)
		default:
			fmt.Fprintf(a.out, "%s ", stamp)
			a.printAutoResult(result, opts.DryRun)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (a *app) printAutoResult(result claudeacct.AutoResult, dryRun bool) {
	d := result.Decision
	switch d.Action {
	case claudeacct.AutoSwitch:
		if dryRun || result.Switched == nil {
			fmt.Fprintf(a.out, "would switch from slot %d (%.0f%%) to slot %d (%.0f%%): %s\n", d.From, d.ActiveMax, d.To, d.TargetMax, d.Reason)
			return
		}
		a.printSwitchNotes(*result.Switched)
		fmt.Fprintf(a.out, "switched from slot %d (%.0f%%) to slot %d (%s); running Claude agents pick it up on their next request\n",
			d.From, d.ActiveMax, result.Switched.To.Number, result.Switched.To.Email)
	case claudeacct.AutoNoTarget:
		fmt.Fprintf(a.out, "no viable account to switch to: %s\n", d.Reason)
	default:
		fmt.Fprintf(a.out, "nothing to do: %s\n", d.Reason)
	}
}
