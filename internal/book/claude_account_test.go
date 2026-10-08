package book

import "testing"

func TestEffectiveClaudeAccountInheritance(t *testing.T) {
	fleet := Fleet{
		Agents: map[string]Agent{
			"root":    {Name: "root"},
			"lead":    {Name: "lead", ClaudeAccount: "b@example.com"},
			"worker":  {Name: "worker"},
			"opt-out": {Name: "opt-out", ClaudeAccount: "default"},
			"leaf":    {Name: "leaf"},
			"loop-a":  {Name: "loop-a"},
			"loop-b":  {Name: "loop-b"},
		},
		Parents: map[string]string{
			"lead": "root", "worker": "lead", "opt-out": "lead", "leaf": "opt-out",
			"loop-a": "loop-b", "loop-b": "loop-a",
		},
	}
	for name, want := range map[string][2]string{
		"root":    {"", ""},
		"lead":    {"b@example.com", "lead"},
		"worker":  {"b@example.com", "lead"},
		"opt-out": {"default", "opt-out"},
		"leaf":    {"default", "opt-out"},
		"loop-a":  {"", ""},
		"missing": {"", ""},
	} {
		account, from := fleet.EffectiveClaudeAccount(name)
		if account != want[0] || from != want[1] {
			t.Fatalf("%s: got %q from %q, want %q from %q", name, account, from, want[0], want[1])
		}
	}
}

func TestSetClaudeAccountWritesAndClears(t *testing.T) {
	_, mainPath, probotPath := setupBooks(t)
	paths := []string{mainPath, probotPath}
	if err := SetClaudeAccount(paths, "probot-business", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := findAgent(t, probotPath, "probot-business").ClaudeAccount; got != "b@example.com" {
		t.Fatalf("bound = %q", got)
	}
	fleet, err := LoadFleet(paths)
	if err != nil {
		t.Fatal(err)
	}
	if account, from := fleet.EffectiveClaudeAccount("probot-business"); account != "b@example.com" || from != "probot-business" {
		t.Fatalf("effective = %q %q", account, from)
	}
	if err := SetClaudeAccount(paths, "probot-business", ""); err != nil {
		t.Fatal(err)
	}
	if got := findAgent(t, probotPath, "probot-business").ClaudeAccount; got != "" {
		t.Fatalf("unbound = %q", got)
	}
}
