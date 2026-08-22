package book

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The record fixtures below are trimmed copies of REAL lines, taken from the
// 2026-08-15 lab run (Claude Code 2.1.233, /tmp/claude-0) and from fleet
// transcripts. Only the fields this gate reads are kept; the shapes — where
// stop_reason sits, that the interrupt record carries interruptedMessageId next
// to a text block, that turn_duration is a system subtype — are unchanged.
const (
	recPrompt      = `{"type":"user","promptId":"p1","isSidechain":false,"message":{"role":"user","content":"Bana 500 satirlik uzun bir masal yaz. Hic arac kullanma."},"timestamp":"2026-08-15T17:19:58.854Z"}`
	recAttachment  = `{"type":"attachment","attachment":{"type":"queued_command"},"timestamp":"2026-08-15T17:19:58.853Z"}`
	recAiTitle     = `{"type":"ai-title","aiTitle":"500 satirlik masal","sessionId":"sess-1"}`
	recLastPrompt  = `{"type":"last-prompt","lastPrompt":"Bana 500 satirlik...","sessionId":"sess-1"}`
	recMode        = `{"type":"mode","mode":"default","sessionId":"sess-1"}`
	recPermMode    = `{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"sess-1"}`
	recBridge      = `{"type":"bridge-session","sessionId":"sess-1","bridgeSessionId":"cse_01","lastSequenceNum":0}`
	recQueueOp     = `{"type":"queue-operation","operation":"enqueue","sessionId":"sess-1","content":"<task-notification>"}`
	recThinking    = `{"type":"assistant","isSidechain":false,"message":{"model":"claude-sonnet-5","id":"msg_01","role":"assistant","content":[{"type":"thinking","thinking":""}],"stop_reason":"tool_use"},"timestamp":"2026-08-15T17:24:21.147Z"}`
	recToolUse     = `{"type":"assistant","isSidechain":false,"message":{"model":"claude-sonnet-5","id":"msg_01","role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{}}],"stop_reason":"tool_use"},"timestamp":"2026-08-15T17:24:21.963Z"}`
	recToolResult  = `{"type":"user","promptId":"p1","isSidechain":false,"message":{"role":"user","content":[{"tool_use_id":"toolu_01","type":"tool_result","content":"bitti"}]},"timestamp":"2026-08-15T17:24:21.972Z"}`
	recAnswer      = `{"type":"assistant","isSidechain":false,"message":{"model":"claude-sonnet-5","id":"msg_02","role":"assistant","content":[{"type":"text","text":"# Bulutlarin Terzisi"}],"stop_reason":"end_turn"},"timestamp":"2026-08-15T17:22:26.181Z"}`
	recTurnDone    = `{"type":"system","subtype":"turn_duration","durationMs":147324,"messageCount":7,"isMeta":false,"timestamp":"2026-08-15T17:22:26.224Z"}`
	recInterrupted = `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]},"interruptedMessageId":"msg_03","timestamp":"2026-08-15T15:00:16.776Z"}`
	// The same interrupt WITHOUT the id field: older records are recognised by
	// their text alone.
	recInterruptedText = `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]},"timestamp":"2026-08-01T19:48:53.619Z"}`
	// A Task subagent's own closing message. It lands in the SAME file as the main
	// chain, after the tool_use that started it, and says nothing about the turn
	// that is waiting for it.
	recSidechainDone = `{"type":"assistant","isSidechain":true,"message":{"model":"claude-sonnet-5","id":"msg_04","role":"assistant","content":[{"type":"text","text":"alt gorev bitti"}],"stop_reason":"end_turn"},"timestamp":"2026-08-15T17:30:00.000Z"}`
	// The reminder Claude Code injects when a session is named: a user record that
	// is not a prompt.
	recMetaReminder = `{"type":"user","isMeta":true,"isSidechain":false,"message":{"role":"user","content":"<system-reminder>\nThe user named this session \"kavram-gate\".\n</system-reminder>"},"timestamp":"2026-08-01T05:03:28.842Z"}`
	// A tool result quoting the interrupt marker — this very package was written
	// in a session whose transcript contains that string. It must not close a turn.
	recQuotesInterrupt = `{"type":"user","isSidechain":false,"message":{"role":"user","content":[{"tool_use_id":"toolu_02","type":"tool_result","content":"grep sonucu: [Request interrupted by user]"}]},"timestamp":"2026-08-15T17:40:00.000Z"}`
	// What /compact leaves behind, measured in bp's own control session on
	// 2026-08-22: a boundary, a continuation summary flagged isCompactSummary,
	// and the slash command's local echoes — user records nobody is answering.
	// They held that agent "working" for 6.5 minutes at an empty composer.
	recCompactBoundary = `{"type":"system","subtype":"compact_boundary","content":"Conversation compacted","level":"info","isSidechain":false,"timestamp":"2026-08-22T12:08:19.384Z"}`
	recCompactSummary  = `{"type":"user","isSidechain":false,"isCompactSummary":true,"message":{"role":"user","content":"This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion."},"timestamp":"2026-08-22T12:08:18.945Z"}`
	recCommandName     = `{"type":"user","isSidechain":false,"message":{"role":"user","content":"<command-name>/compact</command-name>\n<command-message>compact</command-message>"},"timestamp":"2026-08-22T12:05:34.049Z"}`
	recCommandStdout   = `{"type":"user","isSidechain":false,"message":{"role":"user","content":"<local-command-stdout>Compacted (ctrl+o to see full summary)</local-command-stdout>"},"timestamp":"2026-08-22T12:08:19.482Z"}`
)

var recordStamp = regexp.MustCompile(`"timestamp":"[^"]*"`)

// restamp rewrites a record fixture's timestamp to `when`, so a test can age
// individual records independently of the file — which is exactly the situation
// the ceiling has to judge (metadata appends keep the FILE fresh while the
// decisive record is days old).
func restamp(record string, when time.Time) string {
	return recordStamp.ReplaceAllString(record, `"timestamp":"`+when.UTC().Format(time.RFC3339Nano)+`"`)
}

// turnFixture writes a transcript for the agent and stamps BOTH clocks TurnOpen
// reads to the same moment, age ago: the file's mtime and every record's own
// timestamp. The fixtures carry the timestamps of the day they were captured,
// and the ceiling now measures against the record, so leaving them literal
// would age every "open" case past the ceiling as soon as the capture day was
// over.
func turnFixture(t *testing.T, agent, folder string, age time.Duration, records ...string) string {
	t.Helper()
	when := time.Now().Add(-age)
	stamped := make([]string, len(records))
	for i, record := range records {
		stamped[i] = restamp(record, when)
	}
	root := writeTranscript(t, agent, folder, stamped...)
	matches, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("fixture layout: %v %v", matches, err)
	}
	if err := os.Chtimes(matches[0], when, when); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTurnOpenPhases(t *testing.T) {
	const folder = "/srv/kavram-main"
	const agent = "kavram-main"
	// The tail every completed turn leaves behind, measured in that order.
	idleTail := []string{recAnswer, recTurnDone, recLastPrompt, recAiTitle, recMode, recPermMode}

	cases := []struct {
		name    string
		age     time.Duration
		records []string
		want    bool
	}{{
		// The blind window itself: the prompt is on disk, the answer is being
		// streamed, and nothing has been written for a minute. The screen shows
		// nothing either — this is the case the whole gate exists for.
		name:    "streaming answer, only the prompt on disk",
		age:     time.Minute,
		records: []string{recMode, recPermMode, recPrompt, recAttachment, recAiTitle},
		want:    true,
	}, {
		name:    "tool running, tool_use unanswered",
		age:     70 * time.Second,
		records: []string{recPrompt, recThinking, recToolUse},
		want:    true,
	}, {
		name:    "tool finished, result waiting for the model",
		age:     30 * time.Second,
		records: []string{recPrompt, recToolUse, recToolResult},
		want:    true,
	}, {
		name:    "turn over",
		age:     time.Minute,
		records: append([]string{recPrompt, recToolUse, recToolResult}, idleTail...),
		want:    false,
	}, {
		// Older builds (2.1.220) write no turn_duration at all; 21 of 60 fleet
		// transcripts end this way. The stop_reason has to carry the verdict alone.
		name:    "turn over, pre-turn_duration build",
		age:     time.Minute,
		records: []string{recPrompt, recAnswer},
		want:    false,
	}, {
		name:    "interrupted turn",
		age:     time.Minute,
		records: []string{recPrompt, recThinking, recInterrupted},
		want:    false,
	}, {
		name:    "interrupted turn, no interruptedMessageId",
		age:     time.Minute,
		records: []string{recPrompt, recThinking, recInterruptedText},
		want:    false,
	}, {
		// A turn that never recorded an end: the pane was killed, or the CLI died
		// mid-turn. Structurally open forever, so only the ceiling can close it —
		// otherwise this agent's queue would never move again.
		name:    "open turn gone cold, past the ceiling",
		age:     turnOpenCeiling + time.Minute,
		records: []string{recPrompt, recToolUse, recToolResult},
		want:    false,
	}, {
		name:    "open turn just inside the ceiling",
		age:     turnOpenCeiling - time.Minute,
		records: []string{recPrompt, recToolUse, recToolResult},
		want:    true,
	}, {
		// Written a second ago: whatever the tail looks like, something is
		// happening right now and the read may have landed inside a write burst.
		name:    "written just now",
		age:     time.Second,
		records: idleTail,
		want:    true,
	}, {
		// The Task subagent case. Its closing message is the LAST record in the
		// file while the main chain still waits on the tool that started it.
		name:    "subagent finished, main chain still waiting",
		age:     time.Minute,
		records: []string{recPrompt, recToolUse, recSidechainDone},
		want:    true,
	}, {
		// A freshly opened, never-prompted session. Reading the injected reminder
		// as a prompt would make every new agent look busy for a quarter of an
		// hour — and bp briefs agents seconds after opening them.
		name:    "session opened, reminder injected, no prompt yet",
		age:     time.Minute,
		records: []string{recMode, recPermMode, recMetaReminder},
		want:    false,
	}, {
		name:    "tool output quoting the interrupt marker",
		age:     time.Minute,
		records: []string{recPrompt, recToolUse, recQuotesInterrupt},
		want:    true,
	}, {
		// Meta records keep being appended after a turn ends (bp's own /rename, the
		// remote-control bridge, a queued command). None of them may overwrite the
		// verdict of the record that knows.
		name:    "meta records appended after the turn closed",
		age:     time.Minute,
		records: append(append([]string{recPrompt}, idleTail...), recBridge, recQueueOp),
		want:    false,
	}, {
		// The 2026-08-22 incident: a manual /compact on an IDLE agent. The last
		// turn closed before the boundary; the compact's own user records must
		// not reopen it, or every compacted agent waits out the ceiling.
		name:    "manual compact on an idle agent",
		age:     time.Minute,
		records: append(append([]string{recPrompt}, idleTail...), recCommandName, recCompactBoundary, recCompactSummary, recCommandStdout),
		want:    false,
	}, {
		// Any local slash command (/model, /rename) echoes the same way.
		name:    "slash command echo is not a prompt",
		age:     time.Minute,
		records: append(append([]string{recPrompt}, idleTail...), recCommandName, recCommandStdout),
		want:    false,
	}, {
		// An auto-compact MID-turn: the tool records before the boundary still
		// know the phase, and skipping the compact records must not blind the
		// gate to them.
		name:    "auto-compact mid-turn stays open",
		age:     time.Minute,
		records: []string{recPrompt, recToolUse, recToolResult, recCompactBoundary, recCompactSummary},
		want:    true,
	}, {
		name:    "unparsable tail",
		age:     time.Minute,
		records: []string{"not json at all", "{ truncated"},
		want:    false,
	}}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := turnFixture(t, agent, folder, testCase.age, testCase.records...)
			if got := TurnOpen(root, folder, agent, time.Now()); got != testCase.want {
				t.Fatalf("TurnOpen = %v, want %v", got, testCase.want)
			}
		})
	}
}

// The regression server-main measured on 2026-08-15: two agents whose
// half-finished turns were DAYS old (compec-mail 08-03, probot-vitrin 08-11)
// read "working", because timestamp-less metadata appends had refreshed the
// file's mtime that afternoon and the ceiling counted from mtime. The decisive
// record's own timestamp is what the ceiling must age — a fresh file must not
// resurrect a dead turn.
func TestTurnOpenCeilingCountsFromTheDecisiveRecord(t *testing.T) {
	const folder = "/srv/kavram-main"
	const agent = "kavram-main"
	deadTurn := time.Now().Add(-72 * time.Hour)
	records := []string{
		restamp(recPrompt, deadTurn),
		restamp(recToolUse, deadTurn),
		restamp(recToolResult, deadTurn), // structurally open, three days dead
		recLastPrompt,                    // the timestamp-less metadata appended today
		recMode,
		recPermMode,
	}
	root := writeTranscript(t, agent, folder, records...)
	matches, err := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("fixture layout: %v %v", matches, err)
	}
	// The metadata touch: the FILE is a minute old, the TURN three days.
	fresh := time.Now().Add(-time.Minute)
	if err := os.Chtimes(matches[0], fresh, fresh); err != nil {
		t.Fatal(err)
	}
	if TurnOpen(root, folder, agent, time.Now()) {
		t.Fatal("a three-day-dead turn read as open because metadata refreshed the file's mtime")
	}
}

func TestTurnOpenUnresolvableIsNeverBusy(t *testing.T) {
	// Every one of these is an uncertainty, and an uncertainty must leave the
	// screen's verdict standing rather than invent a busy agent.
	const folder = "/srv/kavram-main"
	root := turnFixture(t, "kavram-main", folder, time.Minute, recPrompt)
	for _, testCase := range []struct{ name, root, folder, agent string }{
		{"no folder in the agentbook", root, "", "kavram-main"},
		{"folder is not a path", root, "home: kavram", "kavram-main"},
		{"no agent name", root, folder, ""},
		{"no transcript for this agent", root, folder, "some-other-agent"},
		{"no projects directory", filepath.Join(root, "missing"), folder, "kavram-main"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if TurnOpen(testCase.root, testCase.folder, testCase.agent, time.Now()) {
				t.Fatal("an unresolvable target was reported busy")
			}
		})
	}
}

func TestTurnOpenReadsOnlyTheTail(t *testing.T) {
	// The read is bounded, and this is the case that proves it: the only decisive
	// record in the file says OPEN and sits at the head, pushed out of the window
	// by more than a megabyte of padding. Inside the window there is nothing but
	// meta, so the answer is the uncertainty answer — false — and not a verdict
	// dragged in from a part of the file this gate never promised to read.
	const folder = "/srv/kavram-main"
	filler := `{"type":"attachment","attachment":{"content":"` + strings.Repeat("x", 8192) + `"}}`
	records := []string{recPrompt}
	for i := 0; i < turnOpenTailBytes/8192+8; i++ {
		records = append(records, filler)
	}
	root := turnFixture(t, "kavram-main", folder, time.Minute, records...)
	if TurnOpen(root, folder, "kavram-main", time.Now()) {
		t.Fatal("a verdict was taken from beyond the bounded tail")
	}
	// The same file, with the decisive record close enough to the end to be read.
	root = turnFixture(t, "kavram-main", folder, time.Minute, append(records, recPrompt)...)
	if !TurnOpen(root, folder, "kavram-main", time.Now()) {
		t.Fatal("an open turn inside the window was missed")
	}
}

func TestTurnOpenProbeResolvesFolderFromTheBook(t *testing.T) {
	const folder = "/srv/kavram-main"
	root := turnFixture(t, "kavram-main", folder, time.Minute, recPrompt)
	bookPath := filepath.Join(t.TempDir(), "agentbook.json")
	content := `{"agents":[{"name":"kavram-main","folder":"` + folder + `"}]}`
	if err := os.WriteFile(bookPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := TurnOpenProbe([]string{bookPath}, root)
	if !probe("kavram-main") {
		t.Fatal("an open turn was not seen through the agentbook")
	}
	if probe("nobody") {
		t.Fatal("an agent that is not in the book was reported busy")
	}
}
