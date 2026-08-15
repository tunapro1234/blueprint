package book

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	bptmux "blueprint/internal/tmux"
)

// TurnOpen is the SECOND gate on "is this agent working". The first one reads
// the screen (bptmux.Busy), and the screen has a measured blind window: while a
// long assistant message is being STREAMED, the TUI draws no spinner and no
// "esc to interrupt" line at all — nothing a regex could ever match. Measured in
// a lab pane on 2026-08-15 (Claude Code 2.1.233): a single 8373-token answer
// streamed for 147 seconds during which every screen sample read idle. A
// message delivered into that window lands in a working pane.
//
// So when the screen says "idle", the agent's own session file is asked. The
// same measurement gives the rules, and they are structural rather than
// timing-based, because the file is NOT written while a message streams: Claude
// Code buffers a whole assistant message and appends its records when the
// message completes (the two records of that 147-second answer carry timestamps
// 143 seconds apart but hit the disk in the same 43-millisecond burst). The tail
// of the file therefore does not say "how long ago" — it says WHERE IN THE TURN
// the agent is:
//
//	phase                    last decisive record        verdict
//	prompt accepted          user (real prompt)          open
//	thinking / streaming     user (real prompt)          open   (no write at all)
//	tool running             assistant, stop=tool_use    open   (no write for its duration)
//	tool finished            user (tool_result)          open
//	turn over                system/turn_duration        closed
//	turn over (pre-2.1.233)  assistant, stop=end_turn    closed
//	interrupted              user + interruptedMessageId closed
//
// The asymmetry of the decision is the spine of every threshold below. A wrong
// BUSY is harmless — the message waits one more dispatch pass and the queue
// record names the reason — while a wrong IDLE puts text into a pane that is
// mid-turn. Doubt therefore reads as busy: an unknown record type is skipped
// rather than trusted, and a stop_reason nobody has seen before counts as open.
// The one thing that must not happen is doubt lasting forever, which is what
// turnOpenCeiling bounds.
//
// Every uncertainty that is not doubt about the PHASE — no folder in the
// agentbook, no resolvable session file (a Codex pane has none), an unreadable
// or unparsable file — returns false and leaves the screen's verdict standing.
// That is the same philosophy as the delivery witness: this gate may only ever
// ADD busy, never take a verdict away from the gate that came before it.
func TurnOpen(projectsRoot, folder, agent string, now time.Time) bool {
	dir := FirstPath(folder)
	if dir == "" || agent == "" {
		return false
	}
	path, ok := bptmux.ResumeSessionPath(projectsRoot, dir, agent)
	if !ok {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	age := now.Sub(info.ModTime())
	if age < 0 {
		age = 0 // a clock that runs behind the writer's is not evidence of anything
	}
	// The ceiling comes first, and it is what keeps this gate cheap: an agent
	// that has not been written to in a quarter of an hour is answered with a
	// single stat, and that is most of the fleet most of the time.
	if age > turnOpenCeiling {
		return false
	}
	if age <= turnOpenFresh {
		return true
	}
	return tailTurnOpen(path)
}

const (
	// turnOpenCeiling bounds how long a structurally open turn may keep an agent
	// busy. Without it a turn that ended without ever recording an end — a killed
	// pane, a crashed CLI, a session file left mid-turn (one such tail was found
	// in the fleet on 2026-08-15: -srv-compec, last record a tool_result, 17
	// minutes cold) — would block that agent's queue forever.
	//
	// It is measured against the longest blind stretch a HEALTHY turn can have.
	// The lab turn produced 8373 output tokens in 147 seconds (~57 tok/s) with no
	// write; a reply running into the 32k output cap is therefore ~9.5 minutes of
	// silence. 15 minutes clears that with margin and matches msgq's witnessWindow,
	// so no record can be held by this gate longer than the window in which the
	// transcript witness would settle it anyway.
	turnOpenCeiling = 15 * time.Minute
	// turnOpenFresh is the "written just now, do not think about it" shortcut. A
	// turn's records land in one burst (the 16 KB answer, its turn_duration and
	// four meta records were written 43 ms apart), so a read that lands INSIDE a
	// burst can see a tail that is torn: the answer written but its end marker
	// not yet. 5 seconds is a hundred times the measured burst and costs nothing
	// but calling a just-finished agent busy for one heartbeat.
	turnOpenFresh = 5 * time.Second
	// turnOpenTailBytes bounds the read. Only the LAST decisive record matters, so
	// this window is much smaller than TranscriptTailBytes, which has to hold a
	// delivery for fifteen minutes of growth. It still has to be generous: a single
	// tool_result line runs to hundreds of KiB (q163159804), and the record that
	// decides the verdict may be one of those. 1 MiB covers the measured ones and
	// keeps `bp status` at ~1 MiB per agent, read from page cache.
	turnOpenTailBytes = 1024 * 1024
)

// turn verdicts. none means "this record says nothing about the phase".
const (
	turnNone = iota
	turnOpenVerdict
	turnClosedVerdict
)

// tailTurnOpen reads the bounded tail and returns the verdict of the LAST
// decisive record in it. Records are classified going forward and the verdict is
// overwritten, which is the same thing as scanning backwards without having to
// hold the tail in memory.
//
// A tail with nothing decisive in it returns false: that happens when the window
// lands entirely inside one enormous record, and a partial line is not evidence.
func tailTurnOpen(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false
	}
	partial := info.Size() > turnOpenTailBytes
	if partial {
		if _, err := file.Seek(info.Size()-turnOpenTailBytes, io.SeekStart); err != nil {
			return false
		}
	}
	scanner := bufio.NewScanner(file)
	// Same buffer as the delivery witness: transcript lines are single JSON
	// records and a tool result makes them very long.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	if partial {
		scanner.Scan() // discard the partial line the offset landed in
	}
	verdict := turnNone
	for scanner.Scan() {
		if v := classifyTurnRecord(scanner.Bytes()); v != turnNone {
			verdict = v
		}
	}
	return verdict == turnOpenVerdict
}

// turnRecord is the part of a transcript record this gate reads. Everything else
// in the line — usage counters, attachments, the answer itself — is ignored.
type turnRecord struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	// InterruptedMessageID is present ONLY on the record Claude Code writes when
	// a turn is interrupted, which makes it the reliable half of that detection.
	InterruptedMessageID string `json:"interruptedMessageId"`
	Message              struct {
		Role       string          `json:"role"`
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
}

// interruptedPrefix is what the interrupt record carries as its only text block:
// "[Request interrupted by user]" or "[Request interrupted by user for tool
// use]". It is checked on the DECODED content of a user record, never on the raw
// line, because transcripts quote that string all the time — this very package
// was written in a session whose own tool output contains it, and a raw-substring
// test would have read a working agent as idle.
const interruptedPrefix = "[Request interrupted"

// classifyTurnRecord maps one transcript line to a phase verdict.
//
// Records that belong to a Task subagent (isSidechain) are skipped: they run
// INSIDE a tool call of the main chain, so their own end_turn says nothing about
// the turn that is waiting for them — reading one would call a working agent
// idle at exactly the moment it is busiest.
//
// Injected user records (isMeta) are skipped for the mirror-image reason: the
// "<system-reminder> The user named this session ..." line is written when a
// session OPENS, and treating it as a prompt would make every freshly opened
// agent look mid-turn until the ceiling expired — bp opens agents and briefs
// them seconds later, so that false busy would be felt on every open.
//
// Unknown types (bridge-session, queue-operation, ai-title, last-prompt, mode,
// permission-mode, attachment, file-history-snapshot, custom-title, summary, and
// whatever the next Claude Code version adds) fall through as "says nothing".
// That is deliberate: an unrecognised record must not be able to overwrite the
// verdict of the record that actually knows.
func classifyTurnRecord(line []byte) int {
	if !bytes.HasPrefix(bytes.TrimLeft(line, " \t"), []byte("{")) {
		return turnNone
	}
	var record turnRecord
	if json.Unmarshal(line, &record) != nil {
		return turnNone
	}
	if record.IsSidechain {
		return turnNone
	}
	switch record.Type {
	case "system":
		// turn_duration is written the instant a turn ends, and it is the only
		// system record that means anything here (local_command and friends are
		// noise from slash commands).
		if record.Subtype == "turn_duration" {
			return turnClosedVerdict
		}
		return turnNone
	case "user":
		if record.InterruptedMessageID != "" || interruptedText(record.Message.Content) {
			return turnClosedVerdict
		}
		if record.IsMeta {
			return turnNone
		}
		// A real prompt and a tool_result are the same thing to this gate: the
		// agent has been handed something and no answer has been recorded yet.
		return turnOpenVerdict
	case "assistant":
		if turnEndingStop[record.Message.StopReason] {
			return turnClosedVerdict
		}
		// stop_reason "tool_use" is a turn that continues, and an empty or
		// unrecognised one is a doubt — both read as open.
		return turnOpenVerdict
	}
	return turnNone
}

// turnEndingStop lists the stop reasons that hand control back to the human.
// Anything outside it — "tool_use", an absent value, a reason a later model
// introduces — counts as a turn still running, which is the harmless mistake.
var turnEndingStop = map[string]bool{
	"end_turn":      true,
	"stop_sequence": true,
	"max_tokens":    true,
	"refusal":       true,
}

// interruptedText reports whether a user record's content is the interrupt
// marker. The content is a list of text blocks in every interrupt record
// measured, but older records store plain strings, so both shapes are read.
func interruptedText(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		return strings.HasPrefix(strings.TrimSpace(text), interruptedPrefix)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		if block.Type == "text" && strings.HasPrefix(strings.TrimSpace(block.Text), interruptedPrefix) {
			return true
		}
	}
	return false
}

// TurnOpenProbe builds the function msgq.Queue.TurnOpen and the CLI expect. Like
// DeliveryWitness it re-reads the agentbooks on every call, because the fleet
// changes under a long-running daemon, and answers false for anything it cannot
// resolve.
func TurnOpenProbe(agentbooks []string, projectsRoot string) func(agent string) bool {
	return func(agent string) bool {
		fleet, err := LoadFleet(Paths(agentbooks))
		if err != nil {
			return false
		}
		return TurnOpen(projectsRoot, fleet.Agents[agent].Folder, agent, time.Now())
	}
}
