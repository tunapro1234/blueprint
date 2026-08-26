package daemon

import (
	"strings"
	"testing"
	"time"
)

// The two recognition failures this watchdog was built from, plus the cases it
// must stay quiet about. Silence is the whole value here: an alarm that also
// fires on healthy panes gets filtered by its readers, and then the next real
// drift is invisible again.
func TestPaneSanityFindings(t *testing.T) {
	observations := []paneObservation{
		// The 2026-08-25 case: sandbox off, pane says "node", bp says not an agent.
		{Session: "probot-out-codex", Open: true, IsAgent: false, Command: "node", Binary: "codex"},
		// The 2026-08-26 case, and the reason the alarm no longer depends on the
		// binary list: opencode arrived, this watchdog did not know the name, and
		// class A missed the case it was built for one day earlier. An unknown
		// non-shell command in an open agent's pane is enough.
		{Session: "compec-mail-ox", Open: true, IsAgent: false, Command: "opencode"},
		// The server-main case: live Claude pane, annotated folder, no session found.
		{Session: "server-main", Open: true, IsAgent: true, ClaudePane: true, SessionFound: false, Folder: "/srv (home: /srv/server-main)"},
		// Healthy Claude agent.
		{Session: "probot-anket", Open: true, IsAgent: true, ClaudePane: true, SessionFound: true},
		// A recognised Codex pane has no Claude transcript by design.
		{Session: "probot-egitim-cx", Open: true, IsAgent: true, ClaudePane: false, SessionFound: false},
		// A plain shell in a session bp is not being asked to talk to.
		{Session: "scratch", Open: false, IsAgent: false, Command: "claude", Binary: "claude"},
		// An open agent whose pane really is just a shell: the agent exited and
		// left its shell behind, which bp open already handles. Say nothing.
		{Session: "compec-main", Open: true, IsAgent: false, Command: "zsh", Shell: true},
	}
	findings := paneSanityFindings(observations)
	if len(findings) != 3 {
		t.Fatalf("findings=%v, want exactly the three contradictions", findings)
	}
	if got := findings["compec-mail-ox"]; !strings.Contains(got, "opencode") || !strings.Contains(got, "AGENT SAYMIYOR") {
		t.Fatalf("unknown-TUI finding=%q", got)
	}
	if got := findings["probot-out-codex"]; !strings.Contains(got, "AGENT SAYMIYOR") || !strings.Contains(got, "codex") {
		t.Fatalf("codex finding=%q", got)
	}
	if got := findings["server-main"]; !strings.Contains(got, "oturum dosyasini bulamiyor") || !strings.Contains(got, "/srv (home:") {
		t.Fatalf("server-main finding=%q", got)
	}
}

// A finding is reported once, stays quiet through the cooldown, and becomes
// reportable again after the contradiction clears and returns — the relapse is
// news even inside the cooldown window, because the fix was believed to hold.
func TestDueFindingsCooldownAndRelapse(t *testing.T) {
	now := time.Now()
	reported := map[string]string{}
	findings := map[string]string{"probot-out-codex": "drift"}

	if due := dueFindings(findings, reported, now); len(due) != 1 {
		t.Fatalf("first sweep due=%v, want the finding", due)
	}
	if due := dueFindings(findings, reported, now.Add(time.Hour)); len(due) != 0 {
		t.Fatalf("second sweep due=%v, want silence inside the cooldown", due)
	}
	if due := dueFindings(map[string]string{}, reported, now.Add(2*time.Hour)); len(due) != 0 {
		t.Fatalf("recovered sweep due=%v, want silence", due)
	}
	if len(reported) != 0 {
		t.Fatalf("reported=%v, want the recovered agent forgotten", reported)
	}
	if due := dueFindings(findings, reported, now.Add(3*time.Hour)); len(due) != 1 {
		t.Fatalf("relapse due=%v, want the finding again", due)
	}
}

// The binary test reads argv, and it has to tell "running an agent" from
// "mentioning one" — a shell grepping for claude is not a Claude pane.
func TestAgentBinaryIn(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"claude", "--dangerously-skip-permissions"}, "claude"},
		{[]string{"/usr/local/bin/claude", "--resume", "abc"}, "claude"},
		{[]string{"/usr/lib/node_modules/@openai/codex/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex", "--search"}, "codex"},
		{[]string{"node", "/usr/lib/node_modules/@openai/codex/bin/codex.js"}, "codex"},
		{[]string{"python", "/opt/hermes/hermes"}, "hermes"},
		{[]string{"grep", "-rn", "claude", "/srv"}, ""},
		{[]string{"zsh"}, ""},
		{nil, ""},
	} {
		if got := agentBinaryIn(tc.argv); got != tc.want {
			t.Fatalf("agentBinaryIn(%v)=%q, want %q", tc.argv, got, tc.want)
		}
	}
}
