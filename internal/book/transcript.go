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

const (
	// TranscriptTailBytes bounds the reconciliation read. Agent session files run
	// to 100 MB+, and a message that was delivered is at the END of one: only the
	// last 256 KiB are searched, which on these transcripts covers the most recent
	// few hundred records. A delivery older than that window reads as "not found",
	// which falls back to the previous behavior (paste again) rather than to a
	// wrong answer.
	TranscriptTailBytes = 256 * 1024
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
	probe, ok := transcriptProbe(text)
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
		if !bytes.Contains(line, probe) {
			continue
		}
		if recordedAfter(line, cutoff) {
			return true
		}
	}
	return false
}

// transcriptProbe returns the needle to look for: the message's leading
// characters in the JSON-escaped form a transcript record stores them in (so a
// multi-line message, whose newlines are written as \n, is still found). HTML
// escaping is disabled because Claude Code's own writer does not escape <, > or &
// either.
func transcriptProbe(text string) ([]byte, bool) {
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
	return quoted[1 : len(quoted)-1], true // drop the surrounding quotes
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

// DeliveryWitness builds the function msgq.Queue.Witness expects: it resolves the
// target's folder out of the agentbooks on every call, because the fleet changes
// under a long-running daemon.
func DeliveryWitness(agentbooks []string, projectsRoot string) func(string, string, time.Time) bool {
	return func(to, text string, since time.Time) bool {
		fleet, err := LoadFleet(Paths(agentbooks))
		if err != nil {
			return false
		}
		return TranscriptDelivered(projectsRoot, fleet.Agents[to].Folder, to, text, since)
	}
}
