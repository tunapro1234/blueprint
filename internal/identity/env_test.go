package identity

import (
	"os"
	"testing"
)

// Unit fixtures must not inherit the agent running the test suite.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("CODEX_THREAD_ID")
	os.Exit(m.Run())
}
