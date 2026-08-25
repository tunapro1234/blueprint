package book

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"blueprint/internal/cache"
	bptmux "blueprint/internal/tmux"
)

const (
	// TranscriptTailBytes bounds the reconciliation read. Agent session files run
	// to 100 MB+, and a message that was delivered is at the END of one — but "the
	// end" must be sized for how fast these files GROW, not for how many records
	// it takes to hold a delivery. A NoRepaste record waits up to witnessWindow
	// (15 min) for this witness, and an agent mid-task was measured appending
	// ~47 KiB/min with single tool-result lines in the hundreds of KiB: the
	// deliveries of q163159804 sat 426 KiB behind EOF within three hours, past
	// the old 256 KiB window. 4 MiB covers the wait with margin and is still a
	// trivial read. A delivery older than the window reads as "not found", which
	// falls back to the previous behavior (paste again, or close unverified)
	// rather than to a wrong answer.
	TranscriptTailBytes = 4 * 1024 * 1024
	// transcriptProbeMin is the shortest message this witness will look for. Below
	// it a match proves nothing — "/compact" appears in every transcript that ever
	// ran one — and a false "already delivered" would DROP a message, which is
	// worse than delivering it twice. Short messages therefore never reconcile.
	transcriptProbeMin = 24
	// transcriptProbeMax caps the needle. The opening characters of a message are
	// enough to identify it, and a shorter needle survives a transcript that
	// records long content in more than one piece.
	transcriptProbeMax = 400
	// transcriptClockSkew tolerates the small difference between the clock that
	// stamped the queue record and the one that stamped the transcript record.
	transcriptClockSkew = time.Minute
)

// TranscriptDelivered reports whether text reached the agent's own Claude
// transcript at or after `since`.
//
// This is the same principle the rename fix rests on: the TRANSCRIPT is the
// witness, never the screen. A rendered pane cannot tell a message that was
// delivered from one that was pasted and lost, but the agent's session file only
// grows a record for what it actually received.
//
// The timestamp requirement is what makes it safe to act on. Without it, a
// message deliberately sent twice (an announce repeated by hand) would find its
// own earlier copy and the second one would be silently dropped. With it, only an
// arrival AFTER the record was queued counts.
//
// false is returned for every uncertainty: no folder in the agentbook, no
// resolvable transcript (a Codex pane has none at all), a message too short to
// identify, a record without a parsable timestamp, or an unreadable file. Every
// one of those falls back to delivering the message, never to dropping it.
func TranscriptDelivered(projectsRoot, folder, agent, text string, since time.Time) bool {
	dir := FirstPath(folder)
	if dir == "" || agent == "" {
		return false
	}
	path, ok := bptmux.ResumeSessionPath(projectsRoot, dir, agent)
	if !ok {
		return false
	}
	return deliveredIn(path, text, since)
}

// deliveredIn is the scan itself, kept apart from transcript RESOLUTION so a
// second kind of session file can use it. Codex writes JSONL with the same two
// properties this scan needs — one record per line, a "timestamp" field, the
// message text JSON-escaped inside — so the codex witness is the same reader
// pointed at a rollout (see CodexDelivered).
func deliveredIn(path, text string, since time.Time) bool {
	probes, ok := transcriptProbes(text)
	if !ok {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false
	}
	partial := info.Size() > TranscriptTailBytes
	if partial {
		if _, err := file.Seek(info.Size()-TranscriptTailBytes, io.SeekStart); err != nil {
			return false
		}
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	if partial {
		scanner.Scan() // discard the partial line the offset landed in
	}
	cutoff := since.Add(-transcriptClockSkew)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !containsAny(line, probes) {
			continue
		}
		if recordedAfter(line, cutoff) {
			return true
		}
	}
	return false
}

// containsAny reports whether the line holds ANY of the needles. The variants
// differ only in how the line breaks of one and the same message are spelled, so
// a hit on either is a hit on the message.
func containsAny(line []byte, probes [][]byte) bool {
	for _, probe := range probes {
		if bytes.Contains(line, probe) {
			return true
		}
	}
	return false
}

// CanWitness reports whether a text is one this witness could ever recognise —
// i.e. long enough to identify (transcriptProbeMin). It exists so a caller that
// must decide "may I stop re-pasting and wait for the transcript instead?" asks
// the witness itself rather than re-deriving the threshold. A text this returns
// false for will NEVER close a queue record here, so waiting on it would be
// waiting forever.
func CanWitness(text string) bool {
	_, ok := transcriptProbes(text)
	return ok
}

// transcriptProbes returns the needles to look for: the message's leading
// characters in the JSON-escaped forms a transcript record can store them in.
// HTML escaping is disabled because Claude Code's own writer does not escape <,
// > or & either.
//
// There are TWO variants, and the second one is the whole reason this witness
// ever worked at all. A message is delivered with a tmux bracketed paste, and the
// line breaks that arrive that way are recorded by Claude Code as CARRIAGE
// RETURNS: the stored record reads "...yazdim.\r\rBu..." where the message held
// "...yazdim.\n\nBu...". Measured on q163159804 (2026-08-15), where one message
// was delivered three times because this function only ever produced the \n form
// and therefore NO multi-line message could match its own transcript record — the
// one check that was supposed to stop the repeats never fired.
//
// The substitution is done AFTER escaping, on the two-byte sequence \n, which
// inside a JSON string can only be an escaped newline (a bare 0x0A cannot appear
// there). The single dull edge is a message containing a literal backslash-n:
// its escaped form is \\n and the tail of that gets rewritten too, which makes
// the variant needle useless but never wrong — the unmodified first needle is
// always searched as well.
func transcriptProbes(text string) ([][]byte, bool) {
	trimmed := strings.TrimSpace(text)
	runes := []rune(trimmed)
	if len(runes) < transcriptProbeMin {
		return nil, false
	}
	if len(runes) > transcriptProbeMax {
		runes = runes[:transcriptProbeMax]
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(string(runes)); err != nil {
		return nil, false
	}
	quoted := bytes.TrimSpace(buf.Bytes())
	if len(quoted) < 2 {
		return nil, false
	}
	escaped := quoted[1 : len(quoted)-1] // drop the surrounding quotes
	probes := [][]byte{escaped}
	if carriage := bytes.ReplaceAll(escaped, []byte(`\n`), []byte(`\r`)); !bytes.Equal(carriage, escaped) {
		probes = append(probes, carriage)
	}
	return probes, true
}

// recordedAfter reports whether a transcript record carries a timestamp at or
// after cutoff. A record without a readable timestamp cannot prove anything and
// counts as "not after".
func recordedAfter(line []byte, cutoff time.Time) bool {
	var record struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(line, &record) != nil || record.Timestamp == "" {
		return false
	}
	stamp, err := time.Parse(time.RFC3339, record.Timestamp)
	if err != nil {
		return false
	}
	return !stamp.Before(cutoff)
}

// UserRecord is one thing an agent was HANDED, with the moment it was recorded.
// It is the same material the delivery witness searches, read out whole instead of
// matched against one message — which is what a caller needs when the question is
// not "did this arrive" but "does what arrived look like a single message".
type UserRecord struct {
	Timestamp time.Time
	Text      string
}

// RecentUserTexts returns the text of every user record an agent received at or
// after `since`, oldest first.
//
// It reads the same bounded tail as the turn probe (turnOpenTailBytes, 1 MiB): the
// caller looks at a day of DELIVERIES, which are small records, while the megabytes
// in these files are tool results. A record older than the window is simply not
// returned — the caller's own bookkeeping is what makes that safe, since a delivery
// nobody saw in time is not worth reporting a day late.
//
// Skipped on purpose: sidechain records (a Task subagent's own conversation),
// injected meta records (the session-open system reminder), interrupt markers, and
// tool results — none of them is a message somebody sent to this agent. Anything
// that cannot be resolved (no folder, no session file, an unreadable line) yields
// nothing rather than a guess.
func RecentUserTexts(projectsRoot, folder, agent string, since time.Time) []UserRecord {
	dir := FirstPath(folder)
	if dir == "" || agent == "" {
		return nil
	}
	path, ok := bptmux.ResumeSessionPath(projectsRoot, dir, agent)
	if !ok {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	partial := info.Size() > turnOpenTailBytes
	if partial {
		if _, err := file.Seek(info.Size()-turnOpenTailBytes, io.SeekStart); err != nil {
			return nil
		}
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	if partial {
		scanner.Scan() // discard the partial line the offset landed in
	}
	var records []UserRecord
	for scanner.Scan() {
		record, ok := userDelivery(scanner.Bytes())
		if !ok || record.Timestamp.Before(since) {
			continue
		}
		records = append(records, record)
	}
	return records
}

// userDelivery decodes one transcript line into a delivery, or reports that the
// line is not one.
func userDelivery(line []byte) (UserRecord, bool) {
	if !bytes.HasPrefix(bytes.TrimLeft(line, " \t"), []byte("{")) {
		return UserRecord{}, false
	}
	var record turnRecord
	if json.Unmarshal(line, &record) != nil {
		return UserRecord{}, false
	}
	if record.Type != "user" || record.IsSidechain || record.IsMeta {
		return UserRecord{}, false
	}
	if record.InterruptedMessageID != "" || interruptedText(record.Message.Content) {
		return UserRecord{}, false
	}
	text := userContentText(record.Message.Content)
	if strings.TrimSpace(text) == "" {
		return UserRecord{}, false
	}
	stamp, err := time.Parse(time.RFC3339, record.Timestamp)
	if err != nil {
		return UserRecord{}, false
	}
	return UserRecord{Timestamp: stamp, Text: text}, true
}

// userContentText pulls the human-readable text out of a user record's content,
// which is stored either as a plain string (older records) or as a list of blocks.
// Only text blocks are read: a tool_result block is the machine talking to itself.
func userContentText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// DeliveryWitness builds the function msgq.Queue.Witness expects: it resolves the
// target's folder out of the agentbooks on every call, because the fleet changes
// under a long-running daemon.
// TranscriptExists reports whether an agent HAS a transcript the witness could
// ever read. It is not about this message or this moment: a Hermes pane keeps no
// Claude-style session file at all, so for such a target the witness is not
// "slow to confirm", it is permanently absent.
//
// Callers use it to avoid waiting on evidence that cannot arrive (msgq holds an
// unverified record for fifteen minutes to give the witness time — for a target
// with no transcript that wait is pure delay, measured on the Hermes fleet
// 2026-08-23).
func TranscriptExists(agentbooks []string, projectsRoot string) func(string) bool {
	return func(agent string) bool {
		fleet, err := LoadFleet(Paths(agentbooks))
		if err != nil {
			return false
		}
		dir := FirstPath(fleet.Agents[agent].Folder)
		if dir == "" || agent == "" {
			return false
		}
		_, ok := bptmux.ResumeSessionPath(projectsRoot, dir, agent)
		return ok
	}
}

func DeliveryWitness(agentbooks []string, projectsRoot string) func(string, string, time.Time) bool {
	return func(to, text string, since time.Time) bool {
		fleet, err := LoadFleet(Paths(agentbooks))
		if err != nil {
			return false
		}
		return TranscriptDelivered(projectsRoot, fleet.Agents[to].Folder, to, text, since)
	}
}

// CodexDelivered is TranscriptDelivered for a codex agent, whose record is a
// rollout under CODEX_HOME/sessions rather than a Claude transcript.
//
// It closes a class of false alarm rather than adding a new capability: with no
// witness for codex targets, every unverified delivery to one aged out through
// the "may have gone missing, resend if it never arrived" notice. Measured
// 2026-08-25 on q177804375 — probot-out-codex had read the message and acted on
// it (four replies queued) while bp was still telling the sender the delivery
// could not be confirmed. A notice that cries wolf on delivered messages is how
// a fleet learns to ignore the ones that mean it.
//
// The inbound shape, from the live rollout: {"timestamp":"…","type":
// "response_item","payload":{"type":"message","role":"user","content":[{"type":
// "input_text","text":"…"}]}}. The scan is deliberately the SAME one the Claude
// witness uses, probes and clock skew included: a second matching rule would be
// a second thing to keep true.
func CodexDelivered(codexHome, folder, text string, since time.Time) bool {
	path, ok := cache.RolloutPath(codexHome, FirstPath(folder))
	if !ok {
		return false
	}
	return deliveredIn(path, text, since)
}

// CodexTranscriptExists reports whether a codex agent has a readable rollout, so
// the queue knows an unverified record has something to wait FOR.
func CodexTranscriptExists(codexHome, folder string) bool {
	_, ok := cache.RolloutPath(codexHome, FirstPath(folder))
	return ok
}
