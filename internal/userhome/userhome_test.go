package userhome

import (
	"errors"
	"os"
	"os/user"
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func passwd(home string) func() (*user.User, error) {
	return func() (*user.User, error) { return &user.User{Uid: "1234", HomeDir: home}, nil }
}

func noPasswd() (*user.User, error) { return nil, errors.New("no entry") }

func uid() int { return 1234 }

func TestResolveUsesAbsoluteHOME(t *testing.T) {
	got, err := resolve(env(map[string]string{"HOME": "/home/set"}), passwd("/home/passwd"), uid)
	if err != nil || got != "/home/set" {
		t.Fatalf("resolve = %q, %v; want /home/set", got, err)
	}
}

func TestResolveFallsBackToPasswdWhenHOMEEmptyOrUnset(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"unset":    {},
		"empty":    {"HOME": ""},
		"relative": {"HOME": "relative/home"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := resolve(env(values), passwd("/home/passwd"), uid)
			if err != nil || got != "/home/passwd" {
				t.Fatalf("resolve = %q, %v; want /home/passwd", got, err)
			}
		})
	}
}

func TestResolveNeverReturnsRelativePath(t *testing.T) {
	for name, current := range map[string]func() (*user.User, error){
		"no passwd entry":      noPasswd,
		"empty passwd home":    passwd(""),
		"relative passwd home": passwd("home"),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := resolve(env(map[string]string{}), current, uid)
			if err == nil || got != "" {
				t.Fatalf("resolve = %q, %v; want an error and no path", got, err)
			}
			if !strings.Contains(err.Error(), "uid 1234") {
				t.Fatalf("error does not name the uid: %v", err)
			}
		})
	}
}

// The real lookup: whatever HOME this test process has, removing it must still
// give the passwd home of the current uid (systemd units run without HOME).
func TestDirWithoutHOMEUsesUserDatabase(t *testing.T) {
	want, err := user.Current()
	if err != nil || want.HomeDir == "" {
		t.Skipf("no user database entry for this uid: %v", err)
	}
	t.Setenv("HOME", "")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	got, err := Dir()
	if err != nil || got != want.HomeDir {
		t.Fatalf("Dir() without HOME = %q, %v; want %q", got, err, want.HomeDir)
	}
}

func TestSetUserDatabaseForTestingRestores(t *testing.T) {
	t.Run("override", func(t *testing.T) {
		t.Setenv("HOME", "")
		SetUserDatabaseForTesting(t, passwd("/home/fake"))
		if got, err := Dir(); err != nil || got != "/home/fake" {
			t.Fatalf("Dir() = %q, %v; want /home/fake", got, err)
		}
	})
	t.Setenv("HOME", "")
	if got, _ := Dir(); got == "/home/fake" {
		t.Fatal("the test user database outlived its test")
	}
}
