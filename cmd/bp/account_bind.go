package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"

	"blueprint/internal/book"
	"blueprint/internal/claudeacct"
	bptmux "blueprint/internal/tmux"
)

// Account binding: an agentbook entry may name a stored Claude account
// (claudeAccount). The agent and every descendant without a binding of its
// own then run in that account's profile home, so different agent trees use
// different accounts side by side. The binding applies when an agent is
// launched or resumed; a running agent keeps the account it started on.

// accountBinding resolves a selector to the value stored in the agentbook:
// the account's email, which stays valid when slots are renumbered, or
// "default".
func accountBinding(m *claudeacct.Manager, selector string) (string, claudeacct.Slot, error) {
	if strings.EqualFold(strings.TrimSpace(selector), claudeacct.DefaultAccount) {
		return claudeacct.DefaultAccount, claudeacct.Slot{}, nil
	}
	slot, err := m.Resolve(selector)
	if err != nil {
		return "", claudeacct.Slot{}, err
	}
	return slot.Email, slot, nil
}

// effectiveLaunchAccount is the binding a launch of name uses: own when the
// launch sets one, the stored entry's, or else the one parent's tree carries.
func effectiveLaunchAccount(fleet book.Fleet, name, parent, own string) string {
	if own != "" {
		return own
	}
	if account := fleet.Agents[name].ClaudeAccount; account != "" {
		return account
	}
	if parent == "" {
		parent = fleet.Parents[name]
	}
	if parent == "" || parent == name {
		return ""
	}
	account, _ := fleet.EffectiveClaudeAccount(parent)
	return account
}

// applyLaunchAccount sets the account fields of a Claude launch from its
// binding. They are always recomputed, never taken from a recorded launch,
// so a rebinding takes effect on the next resume. A binding that cannot be
// honoured fails the launch rather than silently starting on another account.
func (a *app) applyLaunchAccount(binding string, opts *bptmux.OpenOptions) error {
	opts.ClaudeAccount, opts.ClaudeConfigDir = "", ""
	if opts.Codex || opts.Hermes || opts.OpenCode {
		return nil
	}
	if binding == "" || strings.EqualFold(binding, claudeacct.DefaultAccount) {
		return nil
	}
	if accountGOOS == "darwin" {
		return fmt.Errorf("Claude account %s is bound here, but account profiles are not supported on macOS", binding)
	}
	if a.config.StateDir == "" {
		return fmt.Errorf("Claude account %s is bound here, but bp has no state directory for account profiles", binding)
	}
	dir, slot, err := newAccountManager(a.config).LaunchDir(binding)
	if err != nil {
		return fmt.Errorf("Claude account %s: %w", binding, err)
	}
	opts.ClaudeAccount, opts.ClaudeConfigDir = slot.Email, dir
	return nil
}

// managedLauncherEnv starts the env prefix of a managed launcher. It carries
// bp's own environment, except that a bp running inside an account profile
// hands on the default Claude home, never its profile: an unbound agent
// opened by a bound one must not inherit the account.
func managedLauncherEnv(home string) string {
	launcher := "env"
	configDir, configSet := claudeacct.OSEnv().DefaultConfigDir()
	if _, inherited := os.LookupEnv("CLAUDE_CONFIG_DIR"); inherited && !configSet {
		launcher += " -u CLAUDE_CONFIG_DIR"
	}
	launcher += " BP_HOME=" + quoteShell(home)
	for _, key := range []string{"HOME", "PATH", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "AGENTBOOK"} {
		value, ok := os.LookupEnv(key)
		if key == "CLAUDE_CONFIG_DIR" {
			value, ok = configDir, configSet
		}
		if ok {
			launcher += " " + key + "=" + quoteShell(value)
		}
	}
	return launcher
}

func (a *app) accountBind(m *claudeacct.Manager, args []string) error {
	if len(args) != 2 {
		return errors.New(accountUsage)
	}
	name, selector := args[0], args[1]
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return err
	}
	agent, ok := fleet.Agents[name]
	if !ok {
		return fmt.Errorf("unknown agent: %s", name)
	}
	binding, slot, err := accountBinding(m, selector)
	if err != nil {
		return err
	}
	if err := book.SetClaudeAccount(a.config.Agentbooks, name, binding); err != nil {
		return err
	}
	if binding == claudeacct.DefaultAccount {
		fmt.Fprintf(a.out, "%s and its tree now use the default Claude login\n", name)
	} else {
		fmt.Fprintf(a.out, "%s and its tree are now bound to Claude account %s\n", name, slotLabel(slot))
	}
	if agent.Launch != nil && launchHarnessName(*agent.Launch) != "claude" {
		fmt.Fprintf(a.out, "note: %s itself runs %s; the binding applies to its Claude descendants\n", name, launchHarnessName(*agent.Launch))
	}
	if binding != claudeacct.DefaultAccount {
		profile, err := m.EnsureProfile(selector)
		if err != nil {
			return err
		}
		if !profile.Ready() {
			fmt.Fprintf(a.out, "next: %s\n", profile.Problem())
		}
	}
	fmt.Fprintln(a.out, "running agents keep their account until they are reopened (bp close + bp open --resume)")
	return nil
}

func (a *app) accountUnbind(args []string) error {
	if len(args) != 1 {
		return errors.New(accountUsage)
	}
	name := args[0]
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return err
	}
	if _, ok := fleet.Agents[name]; !ok {
		return fmt.Errorf("unknown agent: %s", name)
	}
	if err := book.SetClaudeAccount(a.config.Agentbooks, name, ""); err != nil {
		return err
	}
	fleet.Agents[name] = withoutAccount(fleet.Agents[name])
	if account, from := fleet.EffectiveClaudeAccount(name); account != "" {
		fmt.Fprintf(a.out, "%s has no binding of its own; it inherits %s from %s\n", name, account, from)
	} else {
		fmt.Fprintf(a.out, "%s has no binding; it uses the default Claude login\n", name)
	}
	return nil
}

func withoutAccount(agent book.Agent) book.Agent {
	agent.ClaudeAccount = ""
	return agent
}

// accountLogin gives an account's profile its own login. The OAuth flow is
// Claude Code's own (`claude auth login`) run inside the profile home; bp
// never copies a stored slot's tokens into a profile.
func (a *app) accountLogin(m *claudeacct.Manager, args []string) error {
	if len(args) != 1 {
		return errors.New(accountUsage)
	}
	profile, err := m.EnsureProfile(args[0])
	if err != nil {
		return err
	}
	if profile.Ready() {
		fmt.Fprintf(a.out, "the profile of %s is already logged in (%s)\n", slotLabel(profile.Slot), profile.TokenStatus)
		return nil
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		return errors.New("claude is not on PATH")
	}
	fmt.Fprintf(a.out, "logging the profile of %s in; sign in as %s in the browser\n", slotLabel(profile.Slot), profile.Slot.Email)
	cmd := exec.CommandContext(a.ctx, claude, "auth", "login", "--claudeai", "--email", profile.Slot.Email)
	env, _ := bptmux.ScrubClaudeSessionEnv(os.Environ())
	cmd.Env = append(withoutEnv(env, "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"), "CLAUDE_CONFIG_DIR="+profile.Dir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, a.out, a.err
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude auth login: %w", err)
	}
	after, err := m.EnsureProfile(args[0])
	if err != nil {
		return err
	}
	if !after.Ready() {
		return errors.New(after.Problem())
	}
	fmt.Fprintf(a.out, "the profile of %s is logged in; agents bound to it can be opened now\n", slotLabel(after.Slot))
	return nil
}

func withoutEnv(env []string, keys ...string) []string {
	kept := env[:0:0]
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		drop := false
		for _, name := range keys {
			drop = drop || key == name
		}
		if !drop {
			kept = append(kept, entry)
		}
	}
	return kept
}

func (a *app) accountProfile(m *claudeacct.Manager, args []string) error {
	flags, positional, err := accountFlags(args, nil, map[string]bool{"--json": true})
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return errors.New(accountUsage)
	}
	var profiles []claudeacct.Profile
	if len(positional) == 1 {
		profile, err := m.EnsureProfile(positional[0])
		if err != nil {
			return err
		}
		profiles = append(profiles, profile)
	} else if profiles, err = m.Profiles(); err != nil {
		return err
	}
	if flags["--json"] != "" {
		return a.accountJSON(profiles)
	}
	if len(profiles) == 0 {
		fmt.Fprintln(a.out, "no account profiles yet; bp account login <N|email|alias> creates one")
		return nil
	}
	for _, profile := range profiles {
		state := "logged in, " + profile.TokenStatus
		if problem := profile.Problem(); problem != "" {
			state = problem
		}
		fmt.Fprintf(a.out, "%s: %s\n  %s\n", slotLabel(profile.Slot), profile.Dir, state)
		if len(profile.Diverged) > 0 {
			fmt.Fprintf(a.out, "  not shared with the default home: %s\n", strings.Join(profile.Diverged, ", "))
		}
	}
	return nil
}

type accountBindingRow struct {
	Agent    string `json:"agent"`
	Harness  string `json:"harness,omitempty"`
	Account  string `json:"account"`
	From     string `json:"from,omitempty"`
	Launched string `json:"launched,omitempty"`
	Pending  bool   `json:"pending,omitempty"`
}

// accountBindings lists every agent an account binding reaches, with the
// account its last launch ran on: a running agent changes account only when
// it is reopened, and pending marks the ones still on another.
func (a *app) accountBindings(args []string) error {
	flags, positional, err := accountFlags(args, nil, map[string]bool{"--json": true})
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		return errors.New(accountUsage)
	}
	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return err
	}
	rows := []accountBindingRow{}
	for _, name := range fleet.SortedNames() {
		agent := fleet.Agents[name]
		account, from := fleet.EffectiveClaudeAccount(name)
		launched, harness := "", ""
		if agent.Launch != nil {
			launched, harness = agent.Launch.ClaudeAccount, launchHarnessName(*agent.Launch)
		}
		if account == "" && launched == "" {
			continue
		}
		row := accountBindingRow{Agent: name, Harness: harness, Account: account, Launched: launched}
		if from != name {
			row.From = from
		}
		if account == claudeacct.DefaultAccount {
			row.Account = ""
		}
		row.Pending = harness == "claude" && !strings.EqualFold(row.Account, launched)
		rows = append(rows, row)
	}
	if flags["--json"] != "" {
		return a.accountJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.out, "no account bindings; every agent uses the default Claude login")
		return nil
	}
	w := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tACCOUNT\tFROM\tLAST LAUNCH")
	for _, row := range rows {
		account, from, launched := orDash(row.Account, "default"), orDash(row.From, "-"), orDash(row.Launched, "default")
		if row.Harness != "claude" && row.Harness != "" {
			launched = row.Harness
		} else if row.Pending {
			launched += " (reopen to apply)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", row.Agent, account, from, launched)
	}
	return w.Flush()
}

func orDash(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func slotLabel(slot claudeacct.Slot) string {
	if slot.Alias != "" {
		return fmt.Sprintf("%d %s (%s)", slot.Number, slot.Alias, slot.Email)
	}
	return fmt.Sprintf("%d (%s)", slot.Number, slot.Email)
}
