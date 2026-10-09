package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Unit fixtures must not inherit the agent running the test suite, and no
// test may resolve the live installation: without BP_HOME, config.Load reads
// /etc/blueprint/home and would reach the server's config and state.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("CODEX_THREAD_ID")
	// A test binary re-run as a child process keeps the temporary BP_HOME its
	// parent test gave it; anything else is replaced by a sandbox.
	if home := os.Getenv("BP_HOME"); home != "" && strings.HasPrefix(filepath.Clean(home), filepath.Clean(os.TempDir())+string(filepath.Separator)) {
		os.Exit(m.Run())
	}
	sandbox, err := os.MkdirTemp("", "bp-cmd-test-home-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("BP_HOME", sandbox+"/.blueprint")
	code := m.Run()
	_ = os.RemoveAll(sandbox)
	os.Exit(code)
}
