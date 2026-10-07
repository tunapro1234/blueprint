package main

import (
	"fmt"
	"strings"

	"blueprint/internal/book"
	bpconfig "blueprint/internal/config"
)

// codexDisabledError explains a refused Codex launch under codex.disabled.
func codexDisabledError(command string) error {
	return fmt.Errorf("%s: Codex is disabled in this installation (codex.disabled); use --claude, or pass --allow-codex to start Codex anyway", command)
}

// doctorCodexPolicyCheck lists registered agents whose recorded launch still
// starts Codex while the policy disables it. Revive and keepalive keep
// honouring those records, so each one is a Codex session waiting to restart.
func doctorCodexPolicyCheck(cfg bpconfig.Config, fleet book.Fleet, selected string) (doctorCheck, bool) {
	if !cfg.CodexDisabled() {
		return doctorCheck{}, false
	}
	var names []string
	for _, name := range fleet.SortedNames() {
		if selected != "" && name != selected {
			continue
		}
		if launch := fleet.Agents[name].Launch; launch != nil && launch.Codex {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return doctorCheck{}, false
	}
	check := doctorCheck{Name: "codex_policy", Warning: true, Related: names,
		Detail: "codex.disabled is set but these agents still have a recorded Codex launch (revive and keepalive restart Codex): " + strings.Join(names, ", "),
		Next:   "bp close <name>, then bp open <name> <directory> --claude --fresh --rebind"}
	if selected != "" {
		check.Agent = selected
	}
	return check, true
}
