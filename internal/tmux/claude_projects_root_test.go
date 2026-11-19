package tmux

import (
	"errors"
	"os"
	"os/user"
	"testing"

	"blueprint/internal/userhome"
)

// withoutHOME removes HOME for one test; t.Setenv restores it afterwards.
func withoutHOME(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", "")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeProjectsRootResolution(t *testing.T) {
	database := func(home string, err error) func() (*user.User, error) {
		return func() (*user.User, error) {
			if err != nil {
				return nil, err
			}
			return &user.User{HomeDir: home}, nil
		}
	}
	t.Run("HOME set", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		t.Setenv("HOME", "/home/env")
		userhome.SetUserDatabaseForTesting(t, database("/home/passwd", nil))
		if got := ClaudeProjectsRoot(); got != "/home/env/.claude/projects" {
			t.Fatalf("root = %q", got)
		}
	})
	t.Run("HOME empty uses user database", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		t.Setenv("HOME", "")
		userhome.SetUserDatabaseForTesting(t, database("/home/passwd", nil))
		if got := ClaudeProjectsRoot(); got != "/home/passwd/.claude/projects" {
			t.Fatalf("root = %q", got)
		}
	})
	t.Run("HOME unset uses user database", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		withoutHOME(t)
		userhome.SetUserDatabaseForTesting(t, database("/home/passwd", nil))
		if got := ClaudeProjectsRoot(); got != "/home/passwd/.claude/projects" {
			t.Fatalf("root = %q", got)
		}
		if got := ClaudeProjectDir("/srv/x"); got != "/home/passwd/.claude/projects/-srv-x" {
			t.Fatalf("project dir = %q", got)
		}
	})
	t.Run("CLAUDE_CONFIG_DIR wins", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "/opt/claude-profile")
		withoutHOME(t)
		userhome.SetUserDatabaseForTesting(t, database("/home/passwd", nil))
		if got := ClaudeProjectsRoot(); got != "/opt/claude-profile/projects" {
			t.Fatalf("root = %q", got)
		}
	})
	t.Run("unknown home is empty, never relative", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		withoutHOME(t)
		userhome.SetUserDatabaseForTesting(t, database("", errors.New("no entry")))
		if got := ClaudeProjectsRoot(); got != "" {
			t.Fatalf("root = %q, want empty", got)
		}
		if got := ClaudeProjectDir("/srv/x"); got != "" {
			t.Fatalf("project dir = %q, want empty", got)
		}
		if _, err := ResolveSessionPath(ClaudeProjectsRoot(), "/srv/x", "agent", ""); !errors.Is(err, ErrClaudeHomeUnknown) {
			t.Fatalf("ResolveSessionPath err = %v, want ErrClaudeHomeUnknown", err)
		}
		if _, err := ClaudeSessionPathForID("", "/srv/x", "00000000-0000-4000-8000-000000000001"); !errors.Is(err, ErrClaudeHomeUnknown) {
			t.Fatalf("ClaudeSessionPathForID err = %v, want ErrClaudeHomeUnknown", err)
		}
		if _, err := ClaudeProcessSession(os.Getpid(), "/srv/x"); !errors.Is(err, ErrClaudeHomeUnknown) {
			t.Fatalf("ClaudeProcessSession err = %v, want ErrClaudeHomeUnknown", err)
		}
		if _, ok := ResumeSessionPath("", "/srv/x", "agent"); ok {
			t.Fatal("ResumeSessionPath resolved against an unknown root")
		}
	})
}
