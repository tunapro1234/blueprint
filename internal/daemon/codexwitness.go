package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"context"

	"blueprint/internal/book"
	bptmux "blueprint/internal/tmux"
)

// codexHomeFor resolves the CODEX_HOME a pane's process actually runs with. The
// environment is read from /proc rather than assumed, because an agent may be
// given its own codex home and the default would then point at another agent's
// sessions — a witness reading the wrong record is worse than no witness.
func codexHomeFor(pid int) string {
	if pid > 0 {
		if data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ")); err == nil {
			for _, entry := range strings.Split(string(data), "\x00") {
				if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok && value != "" {
					return value
				}
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// codexFolder returns the agentbook folder of a codex agent, and false when the
// agent is not one. The pane decides: a codex agent is a session whose pane
// COMMAND could be codex and whose SCREEN confirms it — the same rule the
// delivery path uses, so the witness and the sender never disagree about what
// kind of agent they are talking to.
func (s *Service) codexPane(session string) (int, string, bool) {
	// A watchdog-grade lookup: bounded, read-only, and never fatal.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	fleet, err := book.LoadFleet(book.Paths(s.config.Agentbooks))
	if err != nil {
		return 0, "", false
	}
	folder := fleet.Agents[session].Folder
	if folder == "" {
		return 0, "", false
	}
	process, err := s.tmux.PaneProcess(ctx, session)
	if err != nil || !bptmux.IsCodexCommand(process.Command) {
		return 0, "", false
	}
	pane, err := s.tmux.Capture(ctx, session)
	if err != nil || !bptmux.CodexPane(pane) {
		return 0, "", false
	}
	return process.PID, folder, true
}

// witness answers "did this text reach that agent" for BOTH kinds of agent: the
// Claude transcript first, and a codex rollout when there is no Claude answer.
//
// The codex half exists because its absence was not neutral. Without a witness a
// codex target's unverified record could only age out through the "may have gone
// missing, resend if it never arrived" notice, and on 2026-08-25 that notice went
// to the sender of q177804375 while probot-out-codex had already read the message
// and queued four replies to it. Every such false alarm teaches its readers to
// discount the true ones.
func (s *Service) witness(claude func(string, string, time.Time) bool) func(string, string, time.Time) bool {
	return func(to, text string, since time.Time) bool {
		if claude != nil && claude(to, text, since) {
			return true
		}
		pid, folder, ok := s.codexPane(to)
		if !ok {
			return false
		}
		return book.CodexDelivered(codexHomeFor(pid), folder, text, since)
	}
}

// hasTranscript is the same widening for the other half of the question: an
// unverified record may WAIT for a witness only if one could ever answer.
func (s *Service) hasTranscript(claude func(string) bool) func(string) bool {
	return func(agent string) bool {
		if claude != nil && claude(agent) {
			return true
		}
		pid, folder, ok := s.codexPane(agent)
		if !ok {
			return false
		}
		return book.CodexTranscriptExists(codexHomeFor(pid), folder)
	}
}
