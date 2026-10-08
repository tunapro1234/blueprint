package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/book"
	bptmux "blueprint/internal/tmux"
)

// fakeClaudeLogin puts a `claude` on PATH whose `auth login --email E`
// writes a login for E into $CLAUDE_CONFIG_DIR, as Claude Code does.
func fakeClaudeLogin(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
[ "$1 $2 $3 $4" = "auth login --claudeai --email" ] || { echo "unexpected args: $*" >&2; exit 2; }
[ -n "$CLAUDE_SECURESTORAGE_CONFIG_DIR" ] && { echo "secure storage dir leaked" >&2; exit 3; }
n=$(echo "$5" | sed 's/^user\([0-9]*\)@.*/\1/')
printf '{"claudeAiOauth":{"accessToken":"` + accountSecret + `-profile-access-%s","refreshToken":"` + accountSecret + `-profile-refresh-%s","expiresAt":4102444800000,"subscriptionType":"pro"}}' "$n" "$n" > "$CLAUDE_CONFIG_DIR/.credentials.json"
printf '{"numStartups":1,"oauthAccount":{"accountUuid":"acct-%s","emailAddress":"%s","organizationUuid":"org-%s"}}' "$n" "$5" "$n" > "$CLAUDE_CONFIG_DIR/.claude.json"
echo "Login successful."
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeBindingBook(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "agentbook.json")
	data, _ := json.Marshal(map[string]any{"agents": []any{
		map[string]any{"name": "main", "folder": filepath.Join(dir, "main")},
		map[string]any{"name": "team", "folder": filepath.Join(dir, "main", "team"), "parent": "main",
			"launch": map[string]any{"claudeAccount": ""}},
		map[string]any{"name": "team-worker", "folder": filepath.Join(dir, "main", "team", "worker"), "parent": "team",
			"launch": map[string]any{"claudeAccount": "user1@example.com"}},
		map[string]any{"name": "team-codex", "folder": filepath.Join(dir, "main", "team", "codex"), "parent": "team",
			"launch": map[string]any{"codex": true}},
		map[string]any{"name": "other", "folder": filepath.Join(dir, "main", "other"), "parent": "main"},
	}})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAccountBindLoginAndLaunch(t *testing.T) {
	e := newAccountEnv(t)
	fakeClaudeLogin(t)
	a := e.app(30)
	e.login(1)
	if out, code := e.run(a, "add", "--alias", "tuna"); code != 0 {
		t.Fatalf("add 1: %s", out)
	}
	e.login(2)
	if out, code := e.run(a, "add", "--alias", "azra"); code != 0 {
		t.Fatalf("add 2: %s", out)
	}
	a.config.Agentbooks = []string{writeBindingBook(t, t.TempDir())}

	out, code := e.run(a, "bind", "team", "azra")
	if code != 0 || !strings.Contains(out, "bound to Claude account 2 azra (user2@example.com)") || !strings.Contains(out, "bp account login azra") {
		t.Fatalf("bind: %d %s", code, out)
	}
	if out, code := e.run(a, "bind", "nobody", "azra"); code == 0 || !strings.Contains(out, "unknown agent") {
		t.Fatalf("bind unknown: %d %s", code, out)
	}

	// Not logged in yet: a launch must fail instead of using another account.
	fleet, err := book.LoadFleet(a.config.Agentbooks)
	if err != nil {
		t.Fatal(err)
	}
	binding := effectiveLaunchAccount(fleet, "team-worker", "", "")
	if binding != "user2@example.com" {
		t.Fatalf("inherited binding = %q", binding)
	}
	opts := bptmux.OpenOptions{}
	if err := a.applyLaunchAccount(binding, &opts); err == nil || !strings.Contains(err.Error(), "bp account login azra") {
		t.Fatalf("launch without a profile login: %v", err)
	}

	out, code = e.run(a, "login", "azra")
	if code != 0 || !strings.Contains(out, "Login successful.") || !strings.Contains(out, "is logged in") {
		t.Fatalf("login: %d %s", code, out)
	}
	if out, code := e.run(a, "login", "azra"); code != 0 || !strings.Contains(out, "already logged in") {
		t.Fatalf("second login: %d %s", code, out)
	}
	if !e.liveAccessIs(2) {
		t.Fatal("profile login changed the default home's login")
	}

	if err := a.applyLaunchAccount(binding, &opts); err != nil {
		t.Fatal(err)
	}
	profileDir := filepath.Join(e.state, "claude-accounts", "profiles", "acct-2")
	if opts.ClaudeAccount != "user2@example.com" || opts.ClaudeConfigDir != profileDir {
		t.Fatalf("launch opts = %+v", opts)
	}
	codex := bptmux.OpenOptions{Codex: true, ClaudeConfigDir: "stale"}
	if err := a.applyLaunchAccount(binding, &codex); err != nil || codex.ClaudeConfigDir != "" {
		t.Fatalf("codex launch got a Claude home: %+v %v", codex, err)
	}
	stale := bptmux.OpenOptions{ClaudeConfigDir: "stale"}
	if err := a.applyLaunchAccount("default", &stale); err != nil || stale.ClaudeConfigDir != "" || stale.ClaudeAccount != "" {
		t.Fatalf("default launch kept a recorded home: %+v %v", stale, err)
	}
	if got := effectiveLaunchAccount(fleet, "other", "", ""); got != "" {
		t.Fatalf("sibling tree inherited %q", got)
	}
	if got := effectiveLaunchAccount(fleet, "new-agent", "team", ""); got != "user2@example.com" {
		t.Fatalf("new child of team = %q", got)
	}
	if got := effectiveLaunchAccount(fleet, "new-agent", "team", "default"); got != "default" {
		t.Fatalf("explicit default = %q", got)
	}

	out, code = e.run(a, "bindings")
	if code != 0 {
		t.Fatalf("bindings: %s", out)
	}
	for _, want := range []string{"team         user2@example.com  -     default (reopen to apply)", "team-worker  user2@example.com  team  user1@example.com (reopen to apply)", "team-codex   user2@example.com  team  codex"} {
		if !strings.Contains(out, want) {
			t.Fatalf("bindings missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "other") {
		t.Fatalf("unbound agent listed:\n%s", out)
	}

	out, code = e.run(a, "profile", "--json")
	if code != 0 || !strings.Contains(out, `"hasLogin": true`) || !strings.Contains(out, `"matches": true`) {
		t.Fatalf("profile: %s", out)
	}

	if out, code := e.run(a, "bind", "team-worker", "default"); code != 0 || !strings.Contains(out, "default Claude login") {
		t.Fatalf("bind default: %s", out)
	}
	fleet, _ = book.LoadFleet(a.config.Agentbooks)
	if got := effectiveLaunchAccount(fleet, "team-worker", "", ""); got != "default" {
		t.Fatalf("opt-out = %q", got)
	}
	if out, code := e.run(a, "unbind", "team-worker"); code != 0 || !strings.Contains(out, "inherits user2@example.com from team") {
		t.Fatalf("unbind child: %s", out)
	}
	if out, code := e.run(a, "unbind", "team"); code != 0 || !strings.Contains(out, "uses the default Claude login") {
		t.Fatalf("unbind team: %s", out)
	}
	if strings.Contains(e.transcript.String(), accountSecret) {
		t.Fatalf("a command printed a token:\n%s", e.transcript.String())
	}
}

func TestManagedLauncherEnvNeverHandsOnAProfile(t *testing.T) {
	e := newAccountEnv(t)
	a := e.app(30)
	e.login(1)
	if out, code := e.run(a, "add"); code != 0 {
		t.Fatalf("add: %s", out)
	}
	if got := managedLauncherEnv("/bp"); !strings.Contains(got, "CLAUDE_CONFIG_DIR="+quoteShell(e.configDir)) || strings.Contains(got, "-u CLAUDE_CONFIG_DIR") {
		t.Fatalf("plain launcher = %s", got)
	}
	m := newAccountManager(a.config)
	profile, err := m.EnsureProfile("1")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", profile.Dir)
	if got := managedLauncherEnv("/bp"); strings.Contains(got, profile.Dir) || !strings.Contains(got, "CLAUDE_CONFIG_DIR="+quoteShell(e.configDir)) {
		t.Fatalf("launcher inside a profile = %s", got)
	}

	// A profile made from the ~/.claude default must unset the variable.
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	home := filepath.Join(t.TempDir(), "p")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".bp-account-profile"), []byte(`{"version":1,"email":"x@example.com","accountUuid":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	if got := managedLauncherEnv("/bp"); !strings.HasPrefix(got, "env -u CLAUDE_CONFIG_DIR ") || strings.Contains(got, home) {
		t.Fatalf("launcher inside a ~/.claude profile = %s", got)
	}
}

func TestOpenCommandCarriesProfileHome(t *testing.T) {
	opts := bptmux.OpenOptions{ClaudeConfigDir: "/state/profiles/acct 2"}
	if got := bptmux.ClaudeConfigPrefix(opts); got != "CLAUDE_CONFIG_DIR='/state/profiles/acct 2' " {
		t.Fatalf("prefix = %q", got)
	}
	opts.Codex = true
	if got := bptmux.ClaudeConfigPrefix(opts); got != "" {
		t.Fatalf("codex prefix = %q", got)
	}
}
