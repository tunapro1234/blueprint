// Package lowprio runs background collectors below interactive work.
//
// Periodic jobs (usage pulses, dashboard generation, watchers) are CPU heavy
// but never latency sensitive, so they run under `nice -n Niceness`. The
// niceness is inherited by every child they spawn. Hosts without a nice
// binary run the command unchanged.
package lowprio

import (
	"context"
	"os/exec"
)

// Niceness is the scheduling priority given to background collectors.
const Niceness = "10"

var lookPath = exec.LookPath

// Args returns argv prefixed with nice when it is available.
func Args(args ...string) []string {
	if len(args) == 0 {
		return args
	}
	nice, err := lookPath("nice")
	if err != nil {
		return args
	}
	return append([]string{nice, "-n", Niceness}, args...)
}

// CommandContext is exec.CommandContext for a low-priority command.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	argv := Args(append([]string{name}, args...)...)
	return exec.CommandContext(ctx, argv[0], argv[1:]...)
}
