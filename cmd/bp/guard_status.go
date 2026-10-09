package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"blueprint/internal/audit"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/modules"
)

// guardCoverage is the caveat `bp guard status` always prints, so the
// tripwire is never mistaken for full coverage.
const guardCoverage = "only Claude Code agents opened or restarted with bp open or bp run after the module was enabled; agents already running keep their old settings until reopened, and plain claude sessions and Codex, Hermes or OpenCode agents are not watched"

// guardStatus prints the guard-hooks module state: on or off, mode,
// canaries, coverage, conflicts and recent reach alerts.
func guardStatus(cfg bpconfig.Config, home string, now time.Time, out io.Writer) {
	on := modules.EnabledIn(cfg, modules.GuardHooks)
	if !on {
		fmt.Fprintln(out, "guard-hooks: off (nothing is watched; bp enable guard-hooks turns it on)")
	} else {
		fmt.Fprintln(out, "guard-hooks: on")
	}
	mode, canaries := "observe (alerts only, never blocks)", "none"
	if cfg.GuardHooks != nil {
		if cfg.GuardHooks.Mode == "ask" {
			mode = "ask (asks before a sensitive tool call that follows external input)"
		}
		if len(cfg.GuardHooks.Canaries) > 0 {
			canaries = strings.Join(cfg.GuardHooks.Canaries, ", ")
		}
	}
	fmt.Fprintln(out, "  mode:", mode)
	fmt.Fprintln(out, "  canaries:", canaries)
	fmt.Fprintln(out, "  coverage:", guardCoverage)
	if module, ok := modules.Lookup(modules.GuardHooks); ok && module.Conflict != nil {
		for _, conflict := range module.Conflict(modules.Env{Config: cfg, UserHome: home}) {
			fmt.Fprintln(out, "  not working:", conflict)
		}
	}
	if cfg.StateDir != "" {
		events, err := audit.Read(cfg.StateDir, audit.Filter{Kind: "guard.reach", Since: now.Add(-24 * time.Hour)})
		if err != nil {
			fmt.Fprintln(out, "  alerts: unreadable:", err)
		} else {
			fmt.Fprintf(out, "  alerts in the last 24h: %d (bp audit --kind guard.reach)\n", len(events))
		}
	}
}

func guardStatusMain(stdout, stderr io.Writer) int {
	cfg, err := bpconfig.Load()
	if err != nil {
		fmt.Fprintln(stderr, "bp guard status:", err)
		return 1
	}
	home, _ := os.UserHomeDir()
	guardStatus(cfg, home, time.Now(), stdout)
	return 0
}
