package main

import (
	"os"
	"strconv"
	"strings"
)

// barStatusJob reports whether this process is a tmux #() status job. Only
// those calls read the bar output cache: tmux 3.4 reruns #() on every status
// redraw, and the cache is what stops that loop from recomputing the bar.
// A direct `bp name` or `bp bar` from a person or a script is always fresh, so
// a native /rename or new usage is visible at once.
//
// tmux runs a job as `sh -c <command>` under the server (measured on tmux 3.4
// with dash as /bin/sh: bp <- sh <- "tmux: server"). A shell in a pane is an
// interactive zsh/bash under the same server, so the direct parent tells the two
// apart. Without /proc (macOS) the answer is unknown and the cache stays on;
// its two-second lifetime bounds how stale such a direct call can be.
var barStatusJob = detectBarStatusJob

func detectBarStatusJob() bool {
	parent := os.Getppid()
	comm, ok := procComm(parent)
	if !ok {
		return true
	}
	if strings.HasPrefix(comm, "tmux") {
		return true
	}
	if comm != "sh" && comm != "dash" {
		return false
	}
	grand, ok := procPPid(parent)
	if !ok {
		return false
	}
	comm, ok = procComm(grand)
	return ok && strings.HasPrefix(comm, "tmux")
}

func procComm(pid int) (string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

// procPPid reads PPid from /proc/<pid>/status; stat's fields are ambiguous when
// comm holds spaces or parentheses ("tmux: server").
func procPPid(pid int) (int, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "PPid:")
		if !ok {
			continue
		}
		parent, err := strconv.Atoi(strings.TrimSpace(rest))
		return parent, err == nil
	}
	return 0, false
}
