package daemon

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"blueprint/internal/book"
	bptmux "blueprint/internal/tmux"
)

// --- merge-sanity: did the agent receive ONE message, or two glued together? --
//
// The pane lock (internal/tmux/panelock.go) closes the hole that produced the
// measured merge: two bp processes pasting into one composer, one Enter submitting
// both. This loop is the part that does not trust that fix to be the last word.
// The delivery path has now drifted twice in ways nobody noticed for days, and both
// times the evidence was sitting in the recipient's own transcript — a record that
// held two senders, or an envelope with its opening bracket cut off. Reading that
// evidence costs one bounded file read per agent per hour.
//
// It reports SHAPES, not suspicions, and only the two that were actually measured:
//
//  1. A truncated envelope: the record's first line begins with the TAIL of a bp
//     envelope ("t-business] BUSINESS → OUTREACH — DUR ..."), so a paste landed on
//     top of something that ate its opening bracket (probot-outreach, 2026-08-17
//     12:53).
//  2. Two envelopes at line starts in ONE record: bp stamps exactly one "[sender] "
//     per delivery, so a second one means two deliveries were submitted together.
//
// What is deliberately NOT a shape: a digest followed by an envelope. bp itself
// composes that as one message — `bp msg` prepends the pending-announcement digest
// to the envelope (cmd/bp: formatDigest + "\n\n[" + sender + "] " + message) — and
// the 590-character record that first looked like a merge on 2026-08-17 was byte
// for byte that composition (236 + 2 + 354). Alarming on it would cry wolf on every
// piggybacked digest, and a watchdog that cries wolf is a watchdog nobody reads.
const (
	// mergeSeenMax bounds the memory of what has already been reported. Two hundred
	// keys is far more than a healthy fleet produces in a lifetime and keeps the
	// state file small enough to read during an incident.
	mergeSeenMax = 200
	// mergeMaxPerSweep bounds one sweep's notices. A delivery path that breaks
	// systematically would otherwise turn one hour's sweep into a flood in
	// server-main's own queue — which is itself a delivery channel.
	mergeMaxPerSweep = 5
	// mergeOverlap is how far each sweep reads BEHIND the watermark, so a record
	// whose write races the sweep cannot slip between two of them. Records land
	// within seconds of their timestamps; five minutes is a wide margin, and the
	// seen set already deduplicates whatever the overlap re-reads.
	mergeOverlap = 5 * time.Minute
)

// truncatedEnvelope matches a first line that STARTS with the tail of an envelope:
// the name and "] " are there, the "[" is not. The match is anchored, so a normal
// envelope ("[probot-business] ...") can never hit it.
//
// The shape of the name is what keeps this from firing on prose. It must look like
// an agent name — lower case, starting with a letter, at least three characters —
// so a numbered line ("3] maddesini ekledim") is not mistaken for damage. The
// measured case, "t-business] BUSINESS → OUTREACH ...", is a business agent's name
// with its first two characters eaten.
var truncatedEnvelope = regexp.MustCompile(`^[a-z][a-z0-9._-]{2,23}\] `)

// envelopeLine matches a whole bp envelope at the beginning of a line. Lower case
// only: agent names are lower case, and the restriction keeps quoted prose from
// counting.
var envelopeLine = regexp.MustCompile(`(?m)^\[[a-z0-9._-]{1,24}\] `)

// mergedShape names the delivery defect in a received text, or "" when the text
// looks like one message.
//
// Carriage returns are folded into newlines first, because that is how a bracketed
// paste's line breaks are RECORDED: the transcript stores "...Agu]\r1) (16 Agu..."
// for a message that held newlines (measured on q163159804 and again here). Without
// the folding, every multi-line delivery would look like a single line and neither
// shape could be seen at all.
func mergedShape(text string) string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	// A leading newline is part of the measured damage rather than a reason to look
	// elsewhere: the record began "\nt-business] ...".
	body := strings.TrimLeft(normalized, "\n")
	first := body
	if index := strings.IndexByte(body, '\n'); index >= 0 {
		first = body[:index]
	}
	if truncatedEnvelope.MatchString(first) {
		return "kirpik zarf (ilk satir '[' olmadan ']' ile aciliyor)"
	}
	if len(envelopeLine.FindAllStringIndex(normalized, 3)) >= 2 {
		return "tek kayitta iki [gonderen] zarfi"
	}
	return ""
}

// mergeScan reads the last day of deliveries for every open agent and reports each
// damaged record ONCE, to the log and to server-main.
//
// Errors are swallowed by design: this is a watchdog, and an agent whose transcript
// cannot be resolved (a Codex pane has none) or whose folder is unknown proves
// nothing about the delivery path. The one thing it must not do is fail the sweep it
// shares a loop with.
func (s *Service) mergeScan(sessions []string, state *busySanityState, now time.Time) {
	fleet, err := book.LoadFleet(book.Paths(s.config.Agentbooks))
	if err != nil {
		return
	}
	seen := make(map[string]bool, len(state.MergeSeen))
	for _, key := range state.MergeSeen {
		seen[key] = true
	}
	// The watermark is what makes SILENCE mean something. The first version of this
	// scan looked back a whole window on its first sweep and promptly reported a
	// record from BEFORE the fix it was deployed to watch — technically true,
	// operationally noise, and exactly the alarm-fatigue class the fleet paid for
	// on 2026-08-17 (a chronic condition reported as an incident teaches everyone
	// to ignore the report). So: the first sweep sets the baseline at deploy time
	// and reports NOTHING — the past has already been examined by the humans who
	// shipped the fix — and every later sweep reads only what arrived since the
	// last one. The overlap guards records whose write races the sweep; the seen
	// set makes the overlap harmless.
	since := now.Add(-mergeOverlap)
	if state.MergeWatermark != "" {
		if mark, err := time.Parse(time.RFC3339, state.MergeWatermark); err == nil && mark.Before(since) {
			since = mark
		}
	}
	baseline := state.MergeWatermark == ""
	defer func() {
		state.MergeWatermark = now.Add(-mergeOverlap).UTC().Format(time.RFC3339)
		if len(state.MergeSeen) > mergeSeenMax {
			state.MergeSeen = state.MergeSeen[len(state.MergeSeen)-mergeSeenMax:]
		}
	}()
	if baseline {
		return
	}
	notices := 0
	for _, session := range sessions {
		folder := fleet.Agents[session].Folder
		if folder == "" {
			continue
		}
		for _, record := range book.RecentUserTexts(bptmux.ClaudeProjectsRoot(), folder, session, since) {
			shape := mergedShape(record.Text)
			if shape == "" {
				continue
			}
			key := session + "@" + record.Timestamp.UTC().Format(time.RFC3339)
			if seen[key] {
				continue
			}
			seen[key] = true
			state.MergeSeen = append(state.MergeSeen, key)
			if notices >= mergeMaxPerSweep {
				continue
			}
			notices++
			message := mergeMessage(session, record.Timestamp, shape)
			s.log.Print(message)
			if s.queue != nil {
				if _, err := s.queue.Enqueue("server-main", "bp", message); err != nil {
					s.log.Printf("merge-sanity: alarm could not be queued: %v", err)
				}
			}
		}
	}
}

// mergeMessage is written for a human: which agent, which record, what is wrong
// with it, and what it means for the delivery channel as a whole.
func mergeMessage(agent string, when time.Time, shape string) string {
	return fmt.Sprintf("bp: %s transcript'inde birlesmis/kirpik teslimat kaydi (%s, %s); bp msg teslimat butunlugu bozulmus olabilir — bak: bp q, bp peek %s",
		agent, when.In(time.Local).Format("2006-01-02 15:04"), shape, agent)
}
