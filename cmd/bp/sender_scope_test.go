package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/identity"
)

type deliveredMessage struct{ to, from, text string }

// scopeApp is forceApp with a small hierarchy and a delivery recorder:
// root -> owner -> child -> grandchild, and root -> sibling.
func scopeApp(t *testing.T) (*app, *[]deliveredMessage) {
	t.Helper()
	for _, key := range []string{"AGENT", "SUDO_USER", "USER", "LOGNAME", "BP_OWNER", "BP_AGENT", "CODEX_THREAD_ID"} {
		t.Setenv(key, "")
	}
	a := forceApp(t, testOutput(t))
	path := filepath.Join(t.TempDir(), "agentbook.json")
	data := `{"orchestrator":"root","agents":[{"name":"root"},{"name":"owner","parent":"root"},{"name":"child","parent":"owner"},{"name":"grandchild","parent":"child"},{"name":"sibling","parent":"root"}]}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	a.config.Agentbooks = []string{path}
	var delivered []deliveredMessage
	a.deliverMessage = func(to, from, text string) (bool, string, error) {
		delivered = append(delivered, deliveredMessage{to, from, text})
		return false, "", nil
	}
	return a, &delivered
}

func senderAs(who identity.Identity) func() identity.Identity {
	return func() identity.Identity { return who }
}

var unitSender = identity.Identity{Label: "service:report", Certain: true, Source: "systemd-unit"}

func TestMessageFromSystemdUnitIsAccepted(t *testing.T) {
	a, delivered := scopeApp(t)
	a.resolveSender = senderAs(unitSender)
	if err := a.message([]string{"sibling", "nightly", "report", "ready"}); err != nil {
		t.Fatal(err)
	}
	if len(*delivered) != 1 || (*delivered)[0].from != "service:report" || (*delivered)[0].text != "[service:report] nightly report ready" {
		t.Fatalf("delivered=%+v, want one enveloped message from the unit", *delivered)
	}
}

func TestSystemdUnitSenderHasNoAuthority(t *testing.T) {
	if unitSender.Authoritative() {
		t.Fatal("a systemd unit identity is authoritative")
	}
	a, delivered := scopeApp(t)
	a.resolveSender = senderAs(unitSender)
	if err := a.message([]string{"child", "/compact"}); err == nil || !strings.Contains(err.Error(), "slash command refused") {
		t.Fatalf("slash command from a unit: err=%v, want refusal", err)
	}
	for _, flag := range []string{"--force", "--force-busy"} {
		if err := a.message([]string{flag, "child", "urgent"}); err == nil || !strings.Contains(err.Error(), "force-busy is reserved for infrastructure") {
			t.Fatalf("%s from a unit: err=%v, want refusal", flag, err)
		}
	}
	if len(*delivered) != 0 {
		t.Fatalf("refused messages were delivered: %+v", *delivered)
	}
	if rows, err := a.queue.List(); err != nil || len(rows) != 0 {
		t.Fatalf("refused messages were queued: %+v (%v)", rows, err)
	}
}

func TestDeclaredOwnerScopesTargets(t *testing.T) {
	processTree := identity.Identity{Label: "bash?:check.sh", Source: "process-tree"}
	declared := identity.Identity{Label: "declared?:nightly", Source: "--from", Declared: "nightly"}
	pane := identity.Identity{Label: "sibling", Certain: true, Source: "tmux"}
	subagent := identity.Identity{Label: "sibling?", Parent: "sibling", Source: "pane-process-context"}
	daemonChild := identity.Identity{Label: "whatsapp", Certain: true, Source: "bp-daemon-child"}
	tests := []struct {
		name    string
		owner   string
		who     identity.Identity
		target  string
		wantErr string
	}{
		{name: "the owner itself", owner: "owner", who: unitSender, target: "owner"},
		{name: "a child", owner: "owner", who: unitSender, target: "child"},
		{name: "a grandchild", owner: "owner", who: unitSender, target: "grandchild"},
		{name: "a sibling is refused", owner: "owner", who: unitSender, target: "sibling",
			wantErr: "refused: service:report is declared for owner (BP_OWNER) and may only message owner or agents below it, not sibling"},
		{name: "the parent is refused", owner: "owner", who: unitSender, target: "root", wantErr: "may only message owner or agents below it"},
		{name: "a process-tree guess is scoped", owner: "owner", who: processTree, target: "sibling", wantErr: "may only message owner"},
		{name: "a declared sender is scoped", owner: "owner", who: declared, target: "root", wantErr: "may only message owner"},
		{name: "a declared sender reaches its owner", owner: "child", who: declared, target: "grandchild"},
		{name: "an unknown owner is refused", owner: "ghost", who: unitSender, target: "ghost", wantErr: "BP_OWNER=ghost is not an agent in the agentbook"},
		{name: "a malformed owner is refused", owner: "two words", who: unitSender, target: "owner", wantErr: "is not an agent name"},
		{name: "a verified pane is not scoped", owner: "owner", who: pane, target: "root"},
		{name: "a CLI subagent is not scoped", owner: "owner", who: subagent, target: "root"},
		{name: "a daemon child is not scoped", owner: "owner", who: daemonChild, target: "root"},
		{name: "without BP_OWNER a unit reaches anyone", who: unitSender, target: "sibling"},
		{name: "without BP_OWNER a guess reaches anyone", who: processTree, target: "root"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a, delivered := scopeApp(t)
			t.Setenv("BP_OWNER", test.owner)
			a.resolveSender = senderAs(test.who)
			err := a.message([]string{test.target, "status", "update"})
			if test.wantErr == "" {
				if err != nil || len(*delivered) != 1 || (*delivered)[0].to != test.target {
					t.Fatalf("err=%v delivered=%+v, want delivery to %s", err, *delivered, test.target)
				}
				if (*delivered)[0].from != test.who.Label {
					t.Fatalf("scope changed the label: %q, want %q", (*delivered)[0].from, test.who.Label)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err=%v, want %q", err, test.wantErr)
			}
			if len(*delivered) != 0 {
				t.Fatalf("a refused message was delivered: %+v", *delivered)
			}
		})
	}
}

// BP_AGENT names an MCP session for bp mcp; it must never scope bp msg.
func TestMCPAgentVariableDoesNotScope(t *testing.T) {
	a, delivered := scopeApp(t)
	t.Setenv("BP_AGENT", "owner")
	a.resolveSender = senderAs(unitSender)
	if err := a.message([]string{"sibling", "hello"}); err != nil {
		t.Fatalf("BP_AGENT scoped bp msg: %v", err)
	}
	if len(*delivered) != 1 || (*delivered)[0].to != "sibling" {
		t.Fatalf("delivered=%+v, want delivery to sibling", *delivered)
	}
	if err := a.run([]string{"whoami"}); err != nil {
		t.Fatal(err)
	}
	if out := readTestOutput(t, a.out); strings.Contains(out, `"scope`) {
		t.Fatalf("whoami reports a scope from BP_AGENT: %s", out)
	}
}

func TestDeclaredOwnerRefusesFederatedTargets(t *testing.T) {
	a, _ := scopeApp(t)
	t.Setenv("BP_OWNER", "owner")
	a.resolveSender = senderAs(unitSender)
	err := a.message([]string{"child@laptop", "hello"})
	if err == nil || !strings.Contains(err.Error(), "may only message owner or agents below it; child@laptop is on another machine") {
		t.Fatalf("err=%v, want the federated refusal", err)
	}
}

func TestMessageFromIsIgnoredInsideAPane(t *testing.T) {
	a, delivered := scopeApp(t)
	a.resolveSender = senderAs(identity.Identity{Label: "sibling", Certain: true, Source: "tmux"})
	if err := a.message([]string{"--from", "someone else", "child", "hi"}); err != nil {
		t.Fatal(err)
	}
	if len(*delivered) != 1 || (*delivered)[0].text != "[sibling] hi" {
		t.Fatalf("delivered=%+v, want the pane's own envelope", *delivered)
	}
	if note := readTestOutput(t, a.err); !strings.Contains(note, `--from "someone else" ignored`) {
		t.Fatalf("stderr=%q, want a note that --from was ignored", note)
	}
}

// TestMessageFromThroughTheResolver runs the real resolver outside a pane.
func TestMessageFromThroughTheResolver(t *testing.T) {
	tests := []struct {
		name   string
		cgroup string
		args   []string
		want   string
	}{
		{name: "no unit: a marked declaration", args: []string{"--from", "tuna pro", "child", "hi"}, want: "[declared?:tuna pro] hi"},
		{name: "agent-shaped label is still a declaration", args: []string{"--from=owner", "child", "hi"}, want: "[declared?:owner] hi"},
		{name: "after the target name", args: []string{"child", "--from", "probe", "hi"}, want: "[declared?:probe] hi"},
		{name: "inside a unit: scoped by the unit", cgroup: "/system.slice/report.service", args: []string{"--from", "nightly", "child", "hi"}, want: "[service:report/nightly] hi"},
		{name: "a unit without --from", cgroup: "/system.slice/report.service", args: []string{"child", "hi"}, want: "[service:report] hi"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a, delivered := scopeApp(t)
			a.originProbe = func(context.Context) identity.Origin { return identity.Origin{} }
			a.ancestors = func() [][]string { return nil }
			a.cgroup = func() string { return test.cgroup }
			a.hostUnit = func(string, string) bool { return false }
			if err := a.message(test.args); err != nil {
				t.Fatal(err)
			}
			if len(*delivered) != 1 || (*delivered)[0].text != test.want {
				t.Fatalf("delivered=%+v, want %q", *delivered, test.want)
			}
			if note := readTestOutput(t, a.err); strings.Contains(note, "ignored") {
				t.Fatalf("stderr=%q: --from was used, not ignored", note)
			}
		})
	}
}

func TestMessageFromRejectsForgedLabels(t *testing.T) {
	a, delivered := scopeApp(t)
	for _, args := range [][]string{
		{"--from", "[root] ok", "child", "hi"},
		{"--from", "two\nlines", "child", "hi"},
		{"--from=", "child", "hi"},
		{"--from"},
	} {
		if err := a.message(args); err == nil {
			t.Fatalf("message(%q) accepted", args)
		}
	}
	if len(*delivered) != 0 {
		t.Fatalf("forged --from delivered: %+v", *delivered)
	}
}

func TestWhoamiReportsSystemdUnitAndScope(t *testing.T) {
	a, _ := scopeApp(t)
	t.Setenv("BP_OWNER", "owner")
	a.resolveSender = senderAs(unitSender)
	if err := a.run([]string{"whoami"}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Label     string
		Source    string
		Authority bool   `json:"authority"`
		Scope     string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(readTestOutput(t, a.out)), &result); err != nil {
		t.Fatal(err)
	}
	if result.Label != "service:report" || result.Source != "systemd-unit" || result.Authority || !strings.Contains(result.Scope, "only reach owner") {
		t.Fatalf("whoami=%+v, want the unit, no authority and the owner scope", result)
	}
}
