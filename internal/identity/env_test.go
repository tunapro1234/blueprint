package identity

import (
	"os"
	"testing"
)

// Unit fixtures must not inherit the agent running the test suite.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("CODEX_THREAD_ID")
	// The suite's own systemd unit must not become a sender in any case that
	// does not inject Options.Cgroup.
	selfCgroup = func() string { return "" }
	os.Exit(m.Run())
}
