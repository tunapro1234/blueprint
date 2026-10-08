package lowprio

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestArgsPrefixesNice(t *testing.T) {
	old := lookPath
	defer func() { lookPath = old }()

	lookPath = func(string) (string, error) { return "/usr/bin/nice", nil }
	got := strings.Join(Args("/usr/bin/python3", "gen.py"), " ")
	if got != "/usr/bin/nice -n 10 /usr/bin/python3 gen.py" {
		t.Fatalf("Args = %q", got)
	}
	if len(Args()) != 0 {
		t.Fatal("empty argv grew a nice prefix")
	}

	lookPath = func(string) (string, error) { return "", errors.New("missing") }
	if got := strings.Join(Args("a", "b"), " "); got != "a b" {
		t.Fatalf("Args without nice = %q", got)
	}
}

func TestCommandContextRunsNiced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no nice")
	}
	if _, err := exec.LookPath("nice"); err != nil {
		t.Skip("no nice binary")
	}
	// `nice` with no arguments prints the current niceness.
	out, err := CommandContext(context.Background(), "nice").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got == "0" || got == "" {
		t.Fatalf("child niceness = %q, want raised", got)
	}
}
