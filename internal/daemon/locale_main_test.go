package daemon

import (
	"os"
	"testing"
)

// Without a UTF-8 locale bp's tmux client prepends -u (internal/tmux
// utf8Args); the fake tmux scripts in this package dispatch on "$1", so the
// suite pins a UTF-8 locale instead of depending on the runner's.
func TestMain(m *testing.M) {
	_ = os.Setenv("LC_ALL", "C.UTF-8")
	os.Exit(m.Run())
}
