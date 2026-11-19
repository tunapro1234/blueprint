// Package userhome resolves the current user's home directory, the base of
// every per-user path bp derives (~/.claude, ~/.codex, ~/.blueprint,
// ~/.config/bp, ...).
//
// os.UserHomeDir fails when HOME is unset or empty, which is the normal
// environment of a systemd unit, and callers that ignored the error built
// relative paths such as ".claude/projects" that resolve against the working
// directory. Dir falls back to the passwd entry of the current uid instead and
// never returns a relative path. Explicit overrides (BP_HOME,
// CLAUDE_CONFIG_DIR, CODEX_HOME, configured paths) stay the callers' concern
// and keep taking precedence over anything derived from the home directory.
package userhome

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// Dir returns $HOME when it is an absolute path, otherwise the home directory
// recorded for the current uid in the user database. It fails, rather than
// returning a relative or empty path, when neither is available.
func Dir() (string, error) {
	return resolve(os.Getenv, lookupCurrent, os.Getuid)
}

// lookupCurrent is the user database query behind Dir.
var lookupCurrent = user.Current

// SetUserDatabaseForTesting makes Dir consult current instead of the real user
// database until the test ends, so a test that removes HOME never resolves the
// home of the account running it (which may be a live installation). Tests
// using it must not run in parallel.
func SetUserDatabaseForTesting(t interface {
	Helper()
	Cleanup(func())
}, current func() (*user.User, error)) {
	t.Helper()
	previous := lookupCurrent
	lookupCurrent = current
	t.Cleanup(func() { lookupCurrent = previous })
}

func resolve(getenv func(string) string, current func() (*user.User, error), uid func() int) (string, error) {
	if home := getenv("HOME"); home != "" && filepath.IsAbs(home) {
		return home, nil
	}
	u, err := current()
	if err != nil {
		return "", fmt.Errorf("home directory unknown: HOME is not set to an absolute path and the user database has no entry for uid %d: %w", uid(), err)
	}
	if u.HomeDir == "" || !filepath.IsAbs(u.HomeDir) {
		return "", fmt.Errorf("home directory unknown: HOME is not set to an absolute path and the user database entry for uid %d has home %q", uid(), u.HomeDir)
	}
	return u.HomeDir, nil
}
