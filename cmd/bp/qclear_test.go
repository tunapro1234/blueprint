package main

import (
	"strings"
	"testing"

	"blueprint/internal/identity"
	"blueprint/internal/msgq"
)

func TestQclearAuthorityIsTheVerifiedSenderOrInfrastructure(t *testing.T) {
	a := forceApp(t, testOutput(t))
	record := msgq.Message{ID: "q100000001", To: "worker-2", From: "worker-1"}
	for _, tc := range []struct {
		name string
		who  identity.Identity
		ok   bool
	}{
		{"verified sender", identity.Identity{Label: "worker-1", Certain: true, Source: "tmux"}, true},
		{"sender label, not verified", identity.Identity{Label: "worker-1", Source: "declared"}, false},
		{"sender label from a subagent", identity.Identity{Label: "worker-1", Certain: true, Source: "tmux", Parent: "worker-1"}, false},
		{"another verified agent", identity.Identity{Label: "worker-3", Certain: true, Source: "tmux"}, false},
		{"bp daemon child", identity.Identity{Label: "bp", Certain: true, Source: "bp-daemon-child"}, true},
	} {
		err := a.qclearAuthority(tc.who)(record)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v, want allowed=%v", tc.name, err, tc.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "only the sender (worker-1, verified)") {
			t.Errorf("%s: refusal does not name the rule: %v", tc.name, err)
		}
	}
}

func TestQclearTakesOneChannelAndOnlyDryRun(t *testing.T) {
	a := forceApp(t, testOutput(t))
	for _, args := range [][]string{nil, {"--dry-run"}, {"q1", "q2"}, {"--force", "q1"}} {
		if err := a.qclear(args); err == nil || !strings.Contains(err.Error(), qclearUsage) {
			t.Errorf("qclear %v: err=%v, want the usage", args, err)
		}
	}
}
