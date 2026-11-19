package tmux

import (
	"errors"
	"os/user"
	"testing"

	"blueprint/internal/userhome"
)

func TestCodexProcessHomeWithoutHOME(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	withoutHOME(t)
	userhome.SetUserDatabaseForTesting(t, func() (*user.User, error) { return &user.User{HomeDir: "/home/passwd"}, nil })
	if got := codexProcessInfo(0, t.TempDir()).Home; got != "/home/passwd/.codex" {
		t.Fatalf("Home = %q", got)
	}
	userhome.SetUserDatabaseForTesting(t, func() (*user.User, error) { return nil, errors.New("no entry") })
	if got := codexProcessInfo(0, t.TempDir()).Home; got != "" {
		t.Fatalf("Home = %q, want empty (never relative)", got)
	}
	t.Setenv("CODEX_HOME", "/opt/codex")
	if got := codexProcessInfo(0, t.TempDir()).Home; got != "/opt/codex" {
		t.Fatalf("Home = %q, CODEX_HOME must win", got)
	}
}
