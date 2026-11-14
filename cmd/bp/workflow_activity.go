package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"blueprint/internal/workflow"
)

// activityKeep bounds the tool calls an activity cursor keeps per agent.
// Older calls are folded into a tally, so the counts stay exact.
const activityKeep = 4096

// Activity implements workflow.ActivityMeter for Claude Code agents. It reads
// the transcript that Observe last bound to the agent (the same resolution
// status, delivery and the token meter use) and never looks for a session
// file of its own. Other harnesses, and an agent with no bound transcript,
// are unknown. A bound file that does not exist yet is a session Claude Code
// has not written to: it has made no tool calls.
func (d *workflowDriver) Activity(_ context.Context, agent string, since time.Time) (workflow.Activity, bool, error) {
	d.fleetMu.RLock()
	path := d.transcripts[agent]
	reader := d.activity
	d.fleetMu.RUnlock()
	if path == "" {
		return workflow.Activity{}, false, nil
	}
	if reader == nil {
		d.fleetMu.Lock()
		if d.activity == nil {
			d.activity = newActivityReader(activityKeep)
		}
		reader = d.activity
		d.fleetMu.Unlock()
	}
	activity, err := reader.read(agent, path, since)
	if err != nil {
		return workflow.Activity{}, false, err
	}
	return activity, true, nil
}

// noteTranscript records the Claude Code transcript an observation bound to
// the agent. An observation that could not bind (a blip, an unreadable pane)
// keeps the last binding; another harness or a binding conflict clears it.
func (d *workflowDriver) noteTranscript(agent, runtime, path, thread string, conflicts int) {
	d.fleetMu.Lock()
	defer d.fleetMu.Unlock()
	if d.transcripts == nil {
		d.transcripts = make(map[string]string)
	}
	switch {
	case runtime == "claude" && path != "" && thread != "" && conflicts == 0:
		d.transcripts[agent] = path
	case conflicts > 0 || (runtime != "" && runtime != "claude"):
		delete(d.transcripts, agent)
	}
}

// activityReader keeps one incremental cursor per agent.
type activityReader struct {
	mu      sync.Mutex
	keep    int
	cursors map[string]*activityCursor
}

func newActivityReader(keep int) *activityReader {
	return &activityReader{keep: keep, cursors: map[string]*activityCursor{}}
}

// activityCursor is an agent's tool calls since a time, read incrementally
// from its transcript: a poll parses only the bytes appended since the last
// one, and a partial last line is left for the next poll.
type activityCursor struct {
	since time.Time
	// info identifies the file offset belongs to.
	info    os.FileInfo
	offset  int64
	earlier workflow.ToolTally
	events  []workflow.ToolEvent
	// index maps a kept call's id to its position in events.
	index map[string]int
}

func (r *activityReader) read(agent, path string, since time.Time) (workflow.Activity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cursor := r.cursors[agent]
	if cursor == nil || !cursor.since.Equal(since) {
		cursor = cursor.restart(since)
		r.cursors[agent] = cursor
	}
	if err := cursor.scan(path, r.keep); err != nil {
		return workflow.Activity{}, err
	}
	return workflow.Activity{Earlier: cursor.earlier.Clone(), Events: append([]workflow.ToolEvent(nil), cursor.events...)}, nil
}

// restart moves a cursor to a new window start. A later start on a cursor
// that has folded nothing keeps its offset and drops the calls before it
// (the next unit on the same session); anything else re-scans from the top
// of the file by timestamp.
func (c *activityCursor) restart(since time.Time) *activityCursor {
	if c != nil && since.After(c.since) && c.earlier.Calls() == 0 {
		kept := c.events[:0:0]
		for _, event := range c.events {
			if !event.At.Before(since) {
				kept = append(kept, event)
			}
		}
		next := &activityCursor{since: since, info: c.info, offset: c.offset, events: kept}
		next.reindex()
		return next
	}
	return &activityCursor{since: since, index: map[string]int{}}
}

func (c *activityCursor) reindex() {
	c.index = make(map[string]int, len(c.events))
	for position, event := range c.events {
		if event.ID != "" {
			c.index[event.ID] = position
		}
	}
}

func (c *activityCursor) scan(path string, keep int) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		// Claude Code creates the file with the session's first message.
		// Calls already collected (from a previous file) stay.
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if c.info == nil || !os.SameFile(c.info, info) || info.Size() < c.offset {
		// Another session file (/clear, a resume into a new session id) or a
		// rewritten one: read it from the top by timestamp. Calls collected
		// from the previous file stay; a call copied into the new file is
		// recognized by its id and not counted twice.
		c.offset = 0
	}
	c.info = info
	if _, err := file.Seek(c.offset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(file, 256*1024)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long := append([]byte(nil), line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = reader.ReadSlice('\n')
				long = append(long, line...)
			}
			line = long
		}
		if errors.Is(err, io.EOF) {
			// A line without its newline is still being written.
			return nil
		}
		if err != nil {
			return err
		}
		c.offset += int64(len(line))
		c.consume(line)
		if keep > 0 && len(c.events) > keep {
			c.fold(len(c.events) - keep*3/4)
		}
	}
}

var (
	toolUseMarker    = []byte(`"tool_use"`)
	toolResultMarker = []byte(`"tool_result"`)
)

// consume reads one transcript record. Only the main conversation counts:
// sidechain records are skipped (current Claude Code writes subagents to
// separate files that are never read here). Tool calls come from
// "assistant" records, results from "user" records; other record types
// (api-request blobs repeat earlier content) are ignored.
func (c *activityCursor) consume(line []byte) {
	if !bytes.Contains(line, toolUseMarker) && !bytes.Contains(line, toolResultMarker) {
		return
	}
	var record struct {
		Type        string `json:"type"`
		IsSidechain bool   `json:"isSidechain"`
		Timestamp   string `json:"timestamp"`
		Message     struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &record) != nil || record.IsSidechain || (record.Type != "assistant" && record.Type != "user") {
		return
	}
	var blocks []struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		IsError   bool            `json:"is_error"`
	}
	if json.Unmarshal(record.Message.Content, &blocks) != nil {
		return
	}
	for _, block := range blocks {
		switch {
		case record.Type == "assistant" && block.Type == "tool_use":
			at, err := time.Parse(time.RFC3339Nano, record.Timestamp)
			if err != nil || at.Before(c.since) || block.Name == "" {
				continue
			}
			if _, seen := c.index[block.ID]; seen && block.ID != "" {
				continue
			}
			c.events = append(c.events, workflow.ToolEvent{At: at.UTC(), ID: block.ID, Tool: block.Name, Input: canonicalInputHash(block.Input)})
			if block.ID != "" {
				c.index[block.ID] = len(c.events) - 1
			}
		case record.Type == "user" && block.Type == "tool_result":
			position, ok := c.index[block.ToolUseID]
			if !ok || c.events[position].Outcome != workflow.ToolPending {
				continue
			}
			c.events[position].Outcome = workflow.ToolSucceeded
			if block.IsError {
				c.events[position].Outcome = workflow.ToolFailed
			}
		}
	}
}

// fold moves the oldest count calls into the tally. A folded call whose
// result has not arrived stays pending for good; Claude Code returns every
// result of a turn before the next model call, so only calls whose result
// never arrives are pending this far back.
func (c *activityCursor) fold(count int) {
	for _, event := range c.events[:count] {
		c.earlier.Add(event)
	}
	c.events = append([]workflow.ToolEvent(nil), c.events[count:]...)
	c.reindex()
}

// canonicalInputHash hashes a tool input after re-encoding it with sorted
// keys, so key order and whitespace do not make equal inputs differ.
func canonicalInputHash(raw json.RawMessage) string {
	canonical := []byte(raw)
	if len(bytes.TrimSpace(raw)) == 0 {
		canonical = []byte("null")
	} else {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil {
			if encoded, err := json.Marshal(value); err == nil {
				canonical = encoded
			}
		}
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:16])
}

const guardrailsCheckWarning = "warning: guardrails read tool activity from Claude Code transcripts only. Codex, Hermes, opencode and any other agent run without guardrail enforcement (units.jsonl records one guardrails_unknown event per agent). bp never interrupts a working agent: a halt is announced to the owner while the agent works and ends the unit at its next idle boundary."

func printWorkflowGuardrails(out *os.File, spec *workflow.GuardrailSpec) {
	if spec == nil {
		return
	}
	levels := func(warn, halt *int) string {
		parts := []string{}
		if warn != nil {
			parts = append(parts, fmt.Sprintf("warn %d", *warn))
		}
		if halt != nil {
			parts = append(parts, fmt.Sprintf("halt %d", *halt))
		}
		return strings.Join(parts, ", ")
	}
	if rule := spec.ToolCalls; rule != nil {
		fmt.Fprintf(out, "guardrail %s: %s tool calls per unit\n", workflow.RuleToolCalls, levels(rule.Warn, rule.Halt))
	}
	for _, item := range []struct {
		name string
		rule *workflow.StreakRule
		what string
	}{{workflow.RuleRepeatedToolCall, spec.RepeatedToolCall, "identical calls in a row"}, {workflow.RuleToolFailures, spec.ToolFailures, "failed results in a row"}} {
		if item.rule == nil {
			continue
		}
		scope := "any tool"
		if item.rule.Tool != "" {
			scope = "tool " + item.rule.Tool
		}
		fmt.Fprintf(out, "guardrail %s: %s %s (%s)\n", item.name, levels(item.rule.Warn, item.rule.Halt), item.what, scope)
	}
}

func guardrailMarker(marks []workflow.GuardrailMark) string {
	if len(marks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(marks))
	for _, mark := range marks {
		parts = append(parts, mark.Rule+" "+mark.Level)
	}
	return " [guardrail " + strings.Join(parts, ", ") + "]"
}

func printActivityStats(out *os.File, count int, stats *workflow.ActivityStats) {
	if stats == nil {
		return
	}
	fmt.Fprintf(out, "activity: units=%d/%d median_tool_calls=%.0f p90_tool_calls=%.0f warned=%d halted=%d\n", stats.Units, count, stats.MedianCalls, stats.P90Calls, stats.Warned, stats.Halted)
	if len(stats.HaltsByRule) == 0 {
		return
	}
	rules := make([]string, 0, len(stats.HaltsByRule))
	for rule := range stats.HaltsByRule {
		rules = append(rules, rule)
	}
	sort.Strings(rules)
	parts := make([]string, 0, len(rules))
	for _, rule := range rules {
		parts = append(parts, fmt.Sprintf("%s=%d", rule, stats.HaltsByRule[rule]))
	}
	fmt.Fprintf(out, "guardrail halts: %s\n", strings.Join(parts, " "))
}
