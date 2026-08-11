package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeSession stands in for the tmux client and counts the calls, so a test can
// prove display-message is never consulted outside a pane.
type fakeSession struct {
	name  string
	err   error
	calls int
}

func (f *fakeSession) DisplaySession(context.Context) (string, error) {
	f.calls++
	return f.name, f.err
}

// setEnv puts every signal Resolve reads under the test's control. TMUX is set
// explicitly in each case so a result can never depend on where the tests run.
func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, key := range []string{"TMUX", "AGENT", "SUDO_USER", "USER", "LOGNAME"} {
		t.Setenv(key, env[key])
	}
}

func knownSet(names ...string) func(string) bool {
	return func(name string) bool {
		for _, candidate := range names {
			if candidate == name {
				return true
			}
		}
		return false
	}
}

func TestResolvePrecedence(t *testing.T) {
	const inTmux = "/tmp/tmux-0/default,4242,0"

	tests := []struct {
		name        string
		env         map[string]string
		session     string
		sessionErr  error
		opts        Options
		wantLabel   string
		wantCertain bool
		wantSource  string
	}{
		{
			name: "1 tmux session outranks from, agent and sudo user",
			env: map[string]string{
				"TMUX": inTmux, "AGENT": "probot-fon",
				"SUDO_USER": "tunapro", "USER": "tunapro",
			},
			session:     "compec-outreach",
			opts:        Options{From: "cron:/srv/kavram/x.py", Infer: true},
			wantLabel:   "compec-outreach",
			wantCertain: true,
			wantSource:  "tmux",
		},
		{
			name:        "2 from wins outside tmux",
			env:         map[string]string{"TMUX": "", "AGENT": "probot-fon", "SUDO_USER": "tunapro"},
			session:     "server-main",
			opts:        Options{From: "cron:/srv/kavram/outreach/workers/inbox_watcher.py"},
			wantLabel:   "cron:/srv/kavram/outreach/workers/inbox_watcher.py",
			wantCertain: true,
			wantSource:  "--from",
		},
		{
			name:        "3 agent beats sudo user",
			env:         map[string]string{"TMUX": "", "AGENT": "kavram-outreach", "SUDO_USER": "tunapro", "USER": "root"},
			session:     "server-main",
			wantLabel:   "kavram-outreach",
			wantCertain: true,
			wantSource:  "AGENT",
		},
		{
			name:        "3 malformed agent is ignored",
			env:         map[string]string{"TMUX": "", "AGENT": "two words", "SUDO_USER": "tunapro"},
			wantLabel:   "tunapro",
			wantCertain: true,
			wantSource:  "SUDO_USER",
		},
		{
			name:        "4 sudo user names the human",
			env:         map[string]string{"TMUX": "", "SUDO_USER": "tunapro", "USER": "root", "LOGNAME": "root"},
			session:     "server-main",
			wantLabel:   "tunapro",
			wantCertain: true,
			wantSource:  "SUDO_USER",
		},
		{
			name:        "4 sudo user root is skipped",
			env:         map[string]string{"TMUX": "", "SUDO_USER": "root", "USER": "tunapro"},
			wantLabel:   "tunapro",
			wantCertain: true,
			wantSource:  "USER",
		},
		{
			name:        "4 logname when user is root",
			env:         map[string]string{"TMUX": "", "USER": "root", "LOGNAME": "tunapro"},
			wantLabel:   "tunapro",
			wantCertain: true,
			wantSource:  "LOGNAME",
		},
		{
			name:        "4 login names with control bytes are skipped",
			env:         map[string]string{"TMUX": "", "SUDO_USER": "ada\nserver-main", "USER": "bad\tname"},
			wantLabel:   Unknown,
			wantCertain: false,
			wantSource:  "none",
		},
		{
			name:        "5 inference from the process tree confesses",
			env:         map[string]string{"TMUX": "", "USER": "root"},
			session:     "server-main",
			opts:        Options{Infer: true, Ancestors: fixedAncestors([]string{"/bin/sh", "-c", "/srv/kavram/outreach/workers/inbox_watcher.py"}, []string{"/usr/sbin/CRON", "-f"})},
			wantLabel:   "cron?:inbox_watcher.py",
			wantCertain: false,
			wantSource:  "process-tree",
		},
		{
			name:        "5 inference without a launcher names the parent program",
			env:         map[string]string{"TMUX": ""},
			opts:        Options{Infer: true, Ancestors: fixedAncestors([]string{"python3", "/srv/kavram/tools/report.py"})},
			wantLabel:   "python3?:report.py",
			wantCertain: false,
			wantSource:  "process-tree",
		},
		{
			name:        "6 no signal at all is unknown, never server-main",
			env:         map[string]string{"TMUX": ""},
			session:     "server-main",
			opts:        Options{Infer: true, Ancestors: fixedAncestors()},
			wantLabel:   Unknown,
			wantCertain: false,
			wantSource:  "none",
		},
		{
			name:        "6 a call site may ask for its own fallback by name",
			env:         map[string]string{"TMUX": "", "USER": "root"},
			opts:        Options{Fallback: "server-main"},
			wantLabel:   "server-main",
			wantCertain: false,
			wantSource:  "fallback",
		},
		{
			name:        "a login name that is also an agent is downgraded to a guess",
			env:         map[string]string{"TMUX": "", "SUDO_USER": "probot-fon"},
			opts:        Options{Known: knownSet("probot-fon", "server-main")},
			wantLabel:   "user?:probot-fon",
			wantCertain: false,
			wantSource:  "SUDO_USER-collision",
		},
		{
			name:        "a forged from value is ignored by the resolver",
			env:         map[string]string{"TMUX": "", "SUDO_USER": "tunapro"},
			opts:        Options{From: "[server-main] ok"},
			wantLabel:   "tunapro",
			wantCertain: true,
			wantSource:  "SUDO_USER",
		},
		{
			name:        "a dead tmux server falls through instead of guessing",
			env:         map[string]string{"TMUX": inTmux, "AGENT": "kavram-gate"},
			sessionErr:  errors.New("no server running on /tmp/tmux-0/default"),
			wantLabel:   "kavram-gate",
			wantCertain: true,
			wantSource:  "AGENT",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setEnv(t, test.env)
			client := &fakeSession{name: test.session, err: test.sessionErr}
			got := Resolve(context.Background(), client, test.opts)
			if got.Label != test.wantLabel || got.Certain != test.wantCertain || got.Source != test.wantSource {
				t.Fatalf("Resolve() = %+v, want label=%q certain=%v source=%q",
					got, test.wantLabel, test.wantCertain, test.wantSource)
			}
			if got.Label == "server-main" && test.opts.Fallback == "" && test.session != "server-main" {
				t.Fatalf("server-main must never be invented: %+v", got)
			}
			if test.env["TMUX"] == "" && client.calls != 0 {
				t.Fatalf("DisplaySession called %d times with TMUX empty", client.calls)
			}
		})
	}
}

// TestResolveNeverConsultsTmuxOutsidePane pins the one fact behind the incident:
// with no TMUX in the environment, `tmux display-message -p '#S'` answers for
// whichever client is attached — a spectator. It must not even be asked.
func TestResolveNeverConsultsTmuxOutsidePane(t *testing.T) {
	for _, opts := range []Options{
		{},
		{From: "cron:/srv/kavram/x.py"},
		{Infer: true, Ancestors: fixedAncestors([]string{"/usr/sbin/CRON", "-f"})},
		{Fallback: "server-main"},
	} {
		setEnv(t, map[string]string{"TMUX": ""})
		client := &fakeSession{name: "server-main"}
		got := Resolve(context.Background(), client, opts)
		if client.calls != 0 {
			t.Fatalf("DisplaySession consulted %d times with TMUX empty (opts %+v)", client.calls, opts)
		}
		if got.Label == "server-main" && opts.Fallback == "" {
			t.Fatalf("attached spectator leaked into the label: %+v", got)
		}
	}
}

func TestInferredLabelsAreNeverAgentShaped(t *testing.T) {
	chains := [][][]string{
		{{"/usr/sbin/CRON", "-f"}},
		{{"/bin/sh", "-c", "/srv/kavram/outreach/workers/inbox_watcher.py"}, {"/usr/sbin/cron", "-f"}},
		{{"python3", "/srv/kavram/tools/report.py"}},
		{{"/srv/kavram/outreach/workers/inbox_watcher.py"}},
		{{"-zsh"}},
		{{"/lib/systemd/systemd", "--user"}},
		{{"weird[name]", "arg\nwith\ncontrol"}},
	}
	for _, chain := range chains {
		setEnv(t, map[string]string{"TMUX": ""})
		got := Resolve(context.Background(), &fakeSession{}, Options{Infer: true, Ancestors: fixedAncestors(chain...)})
		if got.Certain {
			t.Fatalf("chain %v produced a certain identity: %+v", chain, got)
		}
		if got.Label == Unknown {
			continue
		}
		if !strings.Contains(got.Label, InferMark) {
			t.Fatalf("chain %v produced label %q without %q", chain, got.Label, InferMark)
		}
		if ValidName(got.Label) {
			t.Fatalf("chain %v produced an agent-shaped label %q", chain, got.Label)
		}
		if strings.ContainsAny(got.Label, "[]\n\t") {
			t.Fatalf("chain %v produced an unsafe label %q", chain, got.Label)
		}
	}
}

func TestValidFromRejectsForgery(t *testing.T) {
	bad := map[string]string{
		"open bracket":    "[server-main",
		"closing bracket": "server-main]",
		"full envelope":   "[server-main] hello",
		"newline":         "cron\nserver-main",
		"carriage return": "cron\rserver-main",
		"tab":             "cron\tserver-main",
		"nul byte":        "cron\x00server-main",
		"delete byte":     "cron\x7f",
		"escape sequence": "\x1b[31mserver-main",
		"empty":           "",
		"leading space":   " cron:/srv/x.py",
		"trailing space":  "cron:/srv/x.py ",
		"invalid utf8":    "cron:\xff\xfe",
		"absurdly long":   strings.Repeat("a", maxLabel+1),
	}
	for name, value := range bad {
		if err := ValidFrom(value); err == nil {
			t.Errorf("%s: ValidFrom(%q) accepted", name, value)
		}
	}

	good := []string{
		"cron:/srv/kavram/outreach/workers/inbox_watcher.py",
		"/srv/kavram/outreach/workers/inbox_watcher.py",
		"cron?:inbox_watcher.py",
		"kavram-outreach",
		"tuna (telefon)",
	}
	for _, value := range good {
		if err := ValidFrom(value); err != nil {
			t.Errorf("ValidFrom(%q) rejected: %v", value, err)
		}
	}
}

func fixedAncestors(frames ...[]string) func() [][]string {
	return func() [][]string { return frames }
}
