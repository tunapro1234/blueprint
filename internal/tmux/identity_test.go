package tmux

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestDisplaySessionTargetsAndProvesCallingPane(t *testing.T) {
	for _, tc := range []struct {
		name, pane, response string
		ok                   bool
	}{
		{"own process", "%42", fmt.Sprintf("agent\t%d\t0", os.Getpid()), true},
		{"stale inherited pane", "%161", "server-main\t99999999\t0", false},
		{"dead pane", "%42", fmt.Sprintf("server-main\t%d\t1", os.Getpid()), false},
		{"missing pane", "", "server-main", false},
		{"malformed pane", "server-main", "server-main", false},
		{"missing server", "%42", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMUX_PANE", tc.pane)
			calls := 0
			c := New()
			c.exec = func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
				calls++
				if !strings.Contains(strings.Join(args, " "), " -t "+tc.pane+" ") {
					t.Fatalf("untargeted pane: %v", args)
				}
				return []byte(tc.response), nil
			}
			got, err := c.DisplaySession(context.Background())
			if (err == nil) != tc.ok || (!tc.ok && got != "") {
				t.Fatalf("%q %v", got, err)
			}
			if tc.pane == "" && calls != 0 {
				t.Fatal("missing pane consulted tmux")
			}
		})
	}
}
