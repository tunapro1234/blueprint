package msgq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	bptmux "blueprint/internal/tmux"
)

type Message struct {
	ID   string  `json:"id"`
	To   string  `json:"to"`
	From string  `json:"from"`
	Msg  string  `json:"msg"`
	TS   float64 `json:"ts"`
	// Reason says WHY the message is still waiting, in words the operator can act
	// on ("composer'da okunamayan bir paste var (chip)"). It is refreshed on every
	// dispatch pass that has to skip the target, and cleared when the pane frees
	// up. Empty means the plain, uninformative-but-honest "still busy": either the
	// record predates this field, or nobody has looked at the pane yet. An empty
	// value therefore keeps the old output exactly as it was.
	//
	// It exists because the worst version of this queue is the SILENT one: a
	// message sat for four days behind bp's own hanging paste while `bp qstat`
	// reported "is still busy" at an idle agent. A reason turns that into a
	// one-glance fix.
	Reason   string  `json:"reason,omitempty"`
	Status   string  `json:"status,omitempty"`
	Finished float64 `json:"finished,omitempty"`
	// Attempts counts the deliveries that came back as PROVEN failures
	// (bptmux.ErrNotReady). It exists to bound them: the same message was pasted
	// into the same pane every 30 seconds for as long as the screen kept
	// producing that verdict (q163159804, 2026-08-15). Three tries is where bp
	// stops trusting the screen and waits for the transcript instead.
	Attempts int `json:"attempts,omitempty"`
	// NoRepaste marks a record that must NEVER be pasted again, whatever the
	// pane looks like. It is set when the text may already be in the agent
	// (an unverified injection) or when repeated attempts could not be verified.
	// Such a record is not closed either: only the transcript witness may settle
	// it, or the timeout below. It is the single flag that separates "we do not
	// know" from "we know it failed".
	NoRepaste bool `json:"noRepaste,omitempty"`
	// Notified records that the SENDER has been told this delivery could not be
	// verified, so the notice goes out exactly once.
	Notified bool `json:"notified,omitempty"`
	// NextTry is the unix time before which no further Send is attempted
	// (exponential backoff after a proven failure). The transcript witness still
	// runs on every pass — waiting to retry is not waiting to notice.
	NextTry float64 `json:"nextTry,omitempty"`
	// Cleanup marks a record the transcript has PROVEN delivered but whose copy is
	// still hanging in the target's composer, unerased. Such a record is not closed
	// and never pasted again: closing it would destroy the only evidence that the
	// hanging text is ours, and the agent's next turn would submit it as a
	// byte-identical duplicate (measured twice on op-main, 2026-08-17, 441
	// characters each). Every later pass retries the clearing; the witnessWindow
	// ceiling closes the record if the pane never frees up.
	Cleanup bool `json:"cleanup,omitempty"`
	// ForceBusy marks a record whose sender KNOWS the target is busy and wants it
	// delivered anyway (bp msg --force-busy). It is a queue flag and nothing else:
	// the record is delivered by the ordinary dispatch pass, under the ordinary
	// pane lock, so the plumbing that needs to jump a busy agent — the WhatsApp
	// bridge carrying Tuna's own messages — no longer has to become a second
	// writer into the pane, which is what it was doing before.
	//
	// What the flag buys is exactly one gate: the busy refusal. Every other
	// protection still applies to it, because those protect what is in the
	// COMPOSER (somebody's half-written line, an unreadable paste chip, our own
	// hanging text) rather than the agent's concentration.
	ForceBusy bool `json:"forceBusy,omitempty"`
	// ForcedAt is the unix time a force record's text reached the pane. It exists
	// for the record BEHIND it: a second forced message must not be pasted into
	// the same TUI window while the first one may still be sitting there
	// unsubmitted, so a follower waits for the witness or, failing that, for
	// forceCooldown measured from this moment.
	ForcedAt float64 `json:"forcedAt,omitempty"`
}

type Queue struct {
	Root string
	Now  func() time.Time
	// Witness, when set, reports whether a message has ALREADY reached the target
	// — read out of the agent's own transcript, not off the screen. Dispatch asks
	// it before pasting a queued message again, because a message can land
	// without the queue ever learning about it: the operator hand-delivered one
	// during the 2026-08 incident and the daemon pasted a second copy on top of
	// it. since is the moment the record was queued; only a later appearance in
	// the transcript counts, so an older, identical message cannot close a fresh
	// record.
	Witness func(to, text string, since time.Time) bool
	// CanWitness reports whether a text is one the Witness could ever recognise.
	// A message too short to identify (a "/compact") will never be closed by the
	// transcript, so keeping its record open "until the witness speaks" would
	// keep it open forever — those records must still take the old, immediate
	// outcome. The dependency runs this way round because msgq must not know what
	// a transcript is; the daemon binds book.CanWitness here.
	CanWitness func(text string) bool
	// TurnOpen is the second busy gate, and it exists because the first one has a
	// measured blind window: while a long answer is STREAMED the pane draws no
	// spinner, so bptmux.Busy reads idle for as long as the streaming lasts (147
	// seconds in the 2026-08-15 lab run). A message dispatched into that window is
	// pasted into a working agent. This one asks the target's own transcript
	// instead of the screen; the daemon binds book.TurnOpenProbe here. A queue
	// with no probe behaves exactly as before.
	TurnOpen func(to string) bool
	mu       sync.Mutex
}

func New(root string) *Queue {
	return &Queue{Root: root, Now: time.Now}
}

func (q *Queue) pending() string { return filepath.Join(q.Root, "pending") }
func (q *Queue) done() string    { return filepath.Join(q.Root, "done") }

const (
	// unverifiedReason is what a record says while the screen could not confirm
	// a delivery and the transcript has not yet spoken. It is a WAIT, not a
	// failure: the text may well be in the agent already, which is exactly why
	// nothing pastes it again.
	//
	// It says PASTE YAPILDI first, because this sentence is now read by a sender
	// that has just been refused a retry (bp msg's duplicate guard quotes the
	// record's status). "teslimat belirsiz" alone left the honest question — did the
	// text reach the pane at all? — unanswered, and a sender who cannot answer it
	// repeats itself somewhere bp cannot see.
	unverifiedReason = "paste yapildi, ekran dogrulayamadi; tekrar paste edilmeyecek, transcript tanigi bekleniyor"
	// hangingPasteReason is the same record once the composer has been LOOKED at
	// and still holds the text: the message never went in, and bp could not press
	// Enter itself. This one names the action instead of the mechanism.
	hangingPasteReason = "mesaj composer'da ASILI (gonderilmemis); bp bitiremedi — pane bosaldiginda tek Enter yeter"
	// exhaustedReason is the other way into the same waiting state: the screen
	// kept claiming a proven failure and three pastes could not be verified.
	// Continuing would only produce more copies of a message that may already
	// have arrived (measured: three copies of one message, q163159804).
	exhaustedReason = "3 deneme dogrulanamadi; tekrar paste edilmeyecek; transcript tanigi bekleniyor"
	// notReadyAttemptMax bounds the proven-failure retries. Three is enough for a
	// transient cause (someone's line in the composer, a redraw) and few enough
	// that a wrong verdict cannot flood a pane.
	notReadyAttemptMax = 3
	// notReadyBackoff is the first pause after a proven failure; it doubles with
	// each further attempt. The old queue retried every dispatch tick (30s) no
	// matter how many times the same verdict came back.
	notReadyBackoff = 60 * time.Second
	// witnessWindow is how long a NoRepaste record is kept open for the
	// transcript witness. Long enough for an agent to finish a turn and for its
	// session file to be flushed; short enough that the operator hears about it
	// while the context is still alive.
	witnessWindow = 15 * time.Minute
	// noticeHeadRunes is how much of the message the sender's notice quotes —
	// enough to recognise WHICH message, not enough to re-deliver it by accident.
	noticeHeadRunes = 60
	// cleanupReason is what a record says while it is delivered-but-uncleared: the
	// transcript proved the text arrived, and a copy of it is still hanging in the
	// composer where nothing but bp may erase it.
	cleanupReason = "teslim edildi (transcript); composer'da asili kopya temizlenemedi, temizlik bekleniyor"
	// forceReason is what a --force-busy record says while it waits. It is written
	// at enqueue time so a force record is never the uninformative "still busy":
	// the whole point of the flag is that busy was expected.
	forceReason = "force: mesgul pane'e oncelikli teslim bekliyor"
	// forceCooldown bounds how long a forced message waits for the forced message
	// before it. Two WhatsApp messages pasted into one TUI window back to back is
	// the 2026-08-17 merge condition itself, so the normal answer is "wait for the
	// witness"; but the witness can stay silent forever (a text too short to
	// recognise, a session file that never flushes) and a forced message exists
	// because somebody is waiting for it. Ninety seconds is long enough for a
	// transcript to be written and short enough that a phone conversation does not
	// stall. It is a ceiling on the WAIT, not a licence to paste: the composer
	// gates below still refuse a window that still holds the earlier text.
	forceCooldown = 90 * time.Second
)

// headOfLineReason names the record a waiting message is queued BEHIND. It is the
// reason a young message does not overtake an old one any more, said in words the
// operator can act on (`bp qcancel` the blocker, or clear its pane).
func headOfLineReason(head string) string {
	return fmt.Sprintf("sirada: onunde %s var", head)
}

// deliveredThisPassReason is the other half of the ordering rule: this target
// already took a message in this pass, so the next one waits for the pass after.
// Two pastes in one pass is how an agent received three messages inside a single
// second (op-main, 2026-08-17 13:03:37) — the second paste goes in before anything
// can witness the first one leaving the composer.
func deliveredThisPassReason(id string) string {
	return fmt.Sprintf("sirada: bu pass'te %s teslim edildi; sonraki pass bekleniyor", id)
}

// Enqueue records a message with no reason attached (the caller does not know why
// the target could not take it, or there is nothing to say).
func (q *Queue) Enqueue(to, from, text string) (string, error) {
	return q.EnqueueReason(to, from, text, "")
}

// EnqueueReason records a message together with the reason its target could not
// take it right now, so `bp q` and `bp qstat` can show WHY it is waiting instead
// of an unqualified "still busy".
func (q *Queue) EnqueueReason(to, from, text, reason string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.enqueueLocked(to, from, text, enqueueOptions{reason: reason})
}

// EnqueueForce records a message that must go in FRONT of its target's line and
// may be delivered into a busy pane (bp msg --force-busy).
//
// It is deliberately an ordinary queue record: delivery still happens in the
// dispatch pass, holding the pane lock, one paste per pass, with every composer
// gate in force. The alternative — the caller typing into the pane itself — is
// what the WhatsApp bridge used to do, and a second writer in a composer is how
// two messages became one on 2026-08-17.
func (q *Queue) EnqueueForce(to, from, text string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.enqueueLocked(to, from, text, enqueueOptions{reason: forceReason, force: true})
}

// EnqueueUnverified records a message that was ALREADY injected into the pane
// but could not be verified. The record exists only so the transcript witness
// can settle it: it is marked NoRepaste, so no pass will ever paste the text a
// second time. Without it an unverified send has no record at all — it is either
// silently lost or silently delivered, and nobody ever learns which.
func (q *Queue) EnqueueUnverified(to, from, text string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.enqueueLocked(to, from, text, enqueueOptions{reason: unverifiedReason, noRepaste: true})
}

// enqueueOptions are the bookkeeping bits an enqueue may put on the new record
// besides the message itself. A struct rather than a row of bare booleans,
// because at the call site "true, false" says nothing about which flag is which.
type enqueueOptions struct {
	reason    string
	noRepaste bool
	force     bool
}

// enqueueLocked is the body of every enqueue, WITHOUT taking q.mu. It exists
// because Dispatch already holds the mutex and must be able to queue a message
// of its own (the notice to a sender whose delivery could not be verified);
// calling the exported entry point from there would deadlock against itself.
func (q *Queue) enqueueLocked(to, from, text string, opts enqueueOptions) (string, error) {
	if err := os.MkdirAll(q.pending(), 0755); err != nil {
		return "", err
	}
	now := q.Now()
	base := fmt.Sprintf("q%09d", now.UnixNano()%1_000_000_000)
	tmp, err := os.CreateTemp(q.pending(), ".pending-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return "", err
	}
	message := Message{ID: base, To: to, From: from, Msg: text, TS: float64(now.UnixNano()) / 1e9,
		Reason: opts.reason, NoRepaste: opts.noRepaste, ForceBusy: opts.force}
	if err = json.NewEncoder(tmp).Encode(message); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	id := base
	for suffix := 1; ; suffix++ {
		path := filepath.Join(q.pending(), id+".json")
		err = os.Link(tmpName, path)
		if errors.Is(err, os.ErrExist) {
			id = fmt.Sprintf("%s-%d", base, suffix)
			message.ID = id
			data, marshalErr := json.Marshal(message)
			if marshalErr != nil {
				return "", marshalErr
			}
			if writeErr := os.WriteFile(tmpName, append(data, '\n'), 0644); writeErr != nil {
				return "", writeErr
			}
			if tempFile, openErr := os.Open(tmpName); openErr == nil {
				_ = tempFile.Sync()
				_ = tempFile.Close()
			}
			continue
		}
		if err != nil {
			return "", err
		}
		if dir, openErr := os.Open(q.pending()); openErr == nil {
			_ = dir.Sync()
			_ = dir.Close()
		}
		return id, nil
	}
}

func read(path string) (Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Message{}, err
	}
	var message Message
	err = json.Unmarshal(data, &message)
	if err == nil && (message.Status == "" || message.Finished == 0) {
		// Read legacy queue records written before the storage keys became English.
		var legacy struct {
			Status   string  `json:"durum"`
			Finished float64 `json:"bitis"`
		}
		if json.Unmarshal(data, &legacy) == nil {
			if message.Status == "" {
				message.Status = legacy.Status
			}
			if message.Finished == 0 {
				message.Finished = legacy.Finished
			}
		}
	}
	message.Status = englishStatus(message.Status)
	return message, err
}

func englishStatus(status string) string {
	switch status {
	case "iletildi":
		return "delivered"
	case "iptal (hedef kapali)":
		return "canceled (target closed)"
	default:
		return status
	}
}

// record is one pending queue file together with the message inside it.
type record struct {
	path string
	Message
}

// badRecord is a pending file that could not be read. It is carried out of
// pendingRecords rather than swallowed, because the callers disagree about it:
// List refuses to show a half-known queue, while Dispatch reports the file and
// keeps delivering everything else.
type badRecord struct {
	path string
	err  error
}

// pendingRecords reads every pending record and returns them in DELIVERY ORDER:
// oldest send time first, the channel id breaking a tie.
//
// The file NAME is deliberately not the sort key any more, and dropping it fixed
// a delivery bug rather than a style problem. Ids are minted as
// q<nanoseconds-WITHIN-the-second> (see enqueueLocked), so "q950734611" is a
// fraction of a second, not a moment in time: sorted as strings, a message sent at
// 12:55 came out of the queue BEFORE two sent at 10:43 (probot-outreach,
// 2026-08-17), which delivered a "DUR/IPTAL" correction after the instruction it
// was cancelling. TS is the moment the message was queued and is what ordering
// must follow; the id decides only ties — the same nanosecond, or a legacy record
// with no usable TS, where the id at least keeps the order stable between passes.
func (q *Queue) pendingRecords() ([]record, []badRecord, error) {
	paths, err := filepath.Glob(filepath.Join(q.pending(), "*.json"))
	if err != nil {
		return nil, nil, err
	}
	records := make([]record, 0, len(paths))
	var bad []badRecord
	for _, path := range paths {
		message, readErr := read(path)
		if readErr != nil {
			bad = append(bad, badRecord{path: path, err: readErr})
			continue
		}
		records = append(records, record{path: path, Message: message})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].TS != records[j].TS {
			return records[i].TS < records[j].TS
		}
		return records[i].ID < records[j].ID
	})
	return records, bad, nil
}

func (q *Queue) List() ([]Message, error) {
	records, bad, err := q.pendingRecords()
	if err != nil {
		return nil, err
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("%s: %w", bad[0].path, bad[0].err)
	}
	messages := make([]Message, 0, len(records))
	for _, rec := range records {
		messages = append(messages, rec.Message)
	}
	return messages, nil
}

func (q *Queue) Status(id string) (string, error) {
	if message, err := read(filepath.Join(q.pending(), id+".json")); err == nil {
		seconds := int(q.Now().Sub(time.Unix(0, int64(message.TS*1e9))).Seconds())
		if seconds < 0 {
			seconds = 0
		}
		// Delivered, but still holding the pane: the queue is not waiting to send
		// this one, it is waiting to CLEAN UP after it. Saying "PENDING" alone here
		// would send the operator looking for a delivery that already happened.
		if message.Cleanup {
			return fmt.Sprintf("DELIVERED (transcript), temizlik bekliyor: %s — %s (%d seconds queued); bak: bp peek %s",
				message.To, message.Reason, seconds, message.To), nil
		}
		// A record nobody will paste again must SAY so. Reporting it as an
		// ordinary "PENDING" would suggest the queue is still trying, and the
		// operator would keep waiting for a delivery that is now in the
		// transcript's hands.
		if message.NoRepaste {
			return fmt.Sprintf("PENDING (yeniden paste edilmeyecek): %s — %s (%d seconds queued); bak: bp peek %s",
				message.To, message.Reason, seconds, message.To), nil
		}
		// A forced record says that it is forced. Plain "PENDING" would read as an
		// ordinary wait, and this one is deliberately out of order: somebody asked
		// for it to jump a busy pane and is waiting to hear whether it did.
		if message.ForceBusy {
			why := message.Reason
			if why == "" {
				why = forceReason
			}
			return fmt.Sprintf("PENDING (FORCE): %s — %s (%d seconds queued); bak: bp peek %s",
				message.To, why, seconds, message.To), nil
		}
		if wait := int(time.Unix(0, int64(message.NextTry*1e9)).Sub(q.Now()).Seconds()); message.NextTry > 0 && wait > 0 {
			return fmt.Sprintf("PENDING: %s — %s (%d seconds queued; %d. deneme, sonraki deneme ~%ds); bak: bp peek %s",
				message.To, message.Reason, seconds, message.Attempts+1, wait, message.To), nil
		}
		// With a known reason, say it and say what to do about it. "is still busy"
		// is kept ONLY for records nobody has a reason for, because that sentence
		// is what made a four-day-old message look like an ordinary wait.
		if message.Reason != "" {
			return fmt.Sprintf("PENDING: %s — %s (%d seconds queued); bak: bp peek %s",
				message.To, message.Reason, seconds, message.To), nil
		}
		return fmt.Sprintf("PENDING: %s is still busy (%d seconds queued)", message.To, seconds), nil
	}
	if message, err := read(filepath.Join(q.done(), id+".json")); err == nil {
		when := time.Unix(0, int64(message.Finished*1e9)).Local().Format("15:04")
		return fmt.Sprintf("%s: %s (at %s)", strings.ToUpper(message.Status), message.To, when), nil
	}
	return fmt.Sprintf("UNKNOWN: %s is not in the records (records older than 2 days are removed)", id), nil
}

type Target interface {
	HasSession(context.Context, string) bool
	Capture(context.Context, string) (string, error)
	CaptureAnsi(context.Context, string) (string, error)
	Send(context.Context, string, string) error
	// SendForce is Send into a pane that is MID-TURN. It is used for exactly one
	// kind of record — a --force-busy one — and it drops exactly one refusal, the
	// busy pane's. Composer safety is unchanged, and so is the verification: a
	// redrawing screen cannot confirm a paste, so this normally answers
	// ErrUnverified and the record goes to the transcript witness rather than
	// being reported as sent.
	SendForce(context.Context, string, string) error
	// ClearDelivered erases a composer that provably holds one of texts, and
	// nothing else. Dispatch calls it in exactly one situation: the transcript
	// proved a message already arrived, and the same text is STILL hanging in the
	// composer. Once the record is closed nothing can recognise that text as ours
	// again, so leaving it would block the target permanently — the deadlock, back
	// from the other end.
	ClearDelivered(context.Context, string, []string) (bool, error)
	// SubmitStuck presses Enter on a composer holding one of texts EXACTLY, and
	// reports whether it cleared. Dispatch uses it for the one case the witness
	// cannot reach: an unverified send still sitting unsubmitted in the box.
	SubmitStuck(context.Context, string, []string) (bool, error)
}

// PendingFor returns the queued message texts for one target, oldest first.
//
// They exist for a single purpose: handing them to the delivery step so it can
// recognise its OWN unsubmitted paste hanging in a composer instead of reading it
// as "the agent is busy" and queueing behind itself — the deadlock that held one
// message for four days.
func (q *Queue) PendingFor(to string) []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	records, _, err := q.pendingRecords()
	if err != nil {
		return nil
	}
	var texts []string
	for _, rec := range records {
		if rec.To != to {
			continue
		}
		// A delivered-but-uncleared record is deliberately NOT offered here. The
		// caller uses these texts to decide what it may press ENTER on, and this
		// one has already been delivered: submitting the copy hanging in the
		// composer would produce the duplicate the Cleanup state exists to prevent.
		// It stays invisible to the send path and is erased by Dispatch instead,
		// which pushes the new message into the queue behind it — the right order.
		if rec.Cleanup {
			continue
		}
		texts = append(texts, rec.Msg)
	}
	return texts
}

// RecentIdentical returns the newest pending record for `to` whose text is
// byte-identical to text and which was queued within the last `within`.
//
// It is the sender-side duplicate guard, and it exists because the two other
// guards cannot see the case that produced measured duplicates: an agent whose
// first `bp msg` came back "TESLIMAT BELIRSIZ" simply sent the same text twice
// more within 33 seconds (probot-business -> op-main, 2026-08-17). All three
// pastes went into a pane that was streaming, so the screen could not confirm
// them and the transcript had not recorded them yet — every copy landed in Claude
// Code's own input queue and the recipient read the same 441 characters three
// times. Nothing downstream can undo that; only refusing to produce the second
// copy can.
func (q *Queue) RecentIdentical(to, text string, within time.Duration) (Message, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	records, _, err := q.pendingRecords()
	if err != nil {
		return Message{}, false
	}
	cutoff := q.Now().Add(-within)
	var newest Message
	found := false
	for _, rec := range records {
		if rec.To != to || rec.Msg != text {
			continue
		}
		if time.Unix(0, int64(rec.TS*1e9)).Before(cutoff) {
			continue
		}
		newest, found = rec.Message, true
	}
	return newest, found
}

// Reason returns the recorded reason a pending message is still waiting, or ""
// when there is none (an unknown id, a closed record, or a record nobody has
// looked at a pane for yet).
func (q *Queue) Reason(id string) string {
	message, err := read(filepath.Join(q.pending(), id+".json"))
	if err != nil {
		return ""
	}
	return message.Reason
}

// CloseDelivered closes the oldest pending record for `to` whose text is exactly
// text, with the given status. It is how a caller reports that a queued message
// left the composer some other way than through Dispatch — for instance because
// the delivery step found it hanging there and pressed Enter on it. Leaving such
// a record open is what makes the queue paste an already-delivered message a
// second time.
func (q *Queue) CloseDelivered(to, text, status string) (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	records, _, err := q.pendingRecords()
	if err != nil {
		return "", false
	}
	// "Oldest" now really means oldest: the records arrive in send-time order, not
	// in the order of a file name that carries a fraction of a second.
	for _, rec := range records {
		if rec.To != to || rec.Msg != text {
			continue
		}
		if err := q.finish(rec.path, rec.Message, status); err != nil {
			return "", false
		}
		return rec.ID, true
	}
	return "", false
}

// Finished reports whether a record has been closed, and with what status. It
// exists for the CLI's machine-readable outcome line: after a forced message's
// immediate dispatch pass, "did it actually go?" is answered by the record, not
// by parsing the pass's report text.
func (q *Queue) Finished(id string) (string, bool) {
	message, err := read(filepath.Join(q.done(), id+".json"))
	if err != nil {
		return "", false
	}
	return message.Status, true
}

// Cancel removes a pending message by channel id, archiving it as canceled.
func (q *Queue) Cancel(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	path := filepath.Join(q.pending(), id+".json")
	message, err := read(path)
	if err != nil {
		return fmt.Errorf("no pending message %s: %w", id, err)
	}
	return q.finish(path, message, "canceled (by operator)")
}

// remember refreshes the reason recorded on a pending message so `bp q` and
// `bp qstat` can name why it is still waiting. It writes only when the reason
// actually changed, so a target that stays busy for an hour is rewritten once,
// not every five seconds. A failure to write is reported but never fatal: the
// message itself is untouched and delivery is unaffected.
func (q *Queue) remember(path string, message Message, reason string, report func(string)) {
	if message.Reason == reason {
		return
	}
	message.Reason = reason
	if err := writePending(path, message); err != nil && report != nil {
		report(fmt.Sprintf("msgq: could not record the reason for %s: %v", message.ID, err))
	}
}

// update writes a pending record whose bookkeeping fields changed (attempts,
// backoff, the never-paste-again flag). Unlike remember it always writes: the
// caller has already decided something must be remembered across passes, and a
// lost write here would restart a retry count that exists to be bounded.
func (q *Queue) update(path string, message Message, report func(string)) {
	if err := writePending(path, message); err != nil && report != nil {
		report(fmt.Sprintf("msgq: could not record the state of %s: %v", message.ID, err))
	}
}

// retryLater records a PROVEN non-delivery and decides when — or whether — the
// message may be pasted again.
//
// The old code simply left the record pending, so the next tick pasted the same
// text into the same pane 30 seconds later, forever. That is survivable when the
// verdict is true (an expired login) and catastrophic when it is not: on
// q163159804 a redrawing pane produced the verdict over and over for a message
// the agent had already taken, and every retry added a copy. Hence both bounds:
// a growing pause between attempts, and a hard stop after notReadyAttemptMax
// where bp stops believing the screen and waits for the transcript instead.
func (q *Queue) retryLater(path string, message Message, cause error, report func(string)) {
	message.Attempts++
	if message.Attempts >= notReadyAttemptMax {
		message.NoRepaste, message.Reason, message.NextTry = true, exhaustedReason, 0
		q.update(path, message, report)
		if report != nil {
			report(fmt.Sprintf("msgq: %s -> %s %d denemede dogrulanamadi (%s); tekrar paste edilmeyecek, transcript tanigi bekleniyor",
				message.ID, message.To, message.Attempts, whyNotDelivered(cause)))
		}
		return
	}
	delay := notReadyBackoff << (message.Attempts - 1)
	message.Reason = whyNotDelivered(cause)
	message.NextTry = float64(q.Now().Add(delay).UnixNano()) / 1e9
	q.update(path, message, report)
	if report != nil {
		report(fmt.Sprintf("msgq: %s -> %s not delivered (%v); still queued, %d. deneme %s sonra",
			message.ID, message.To, cause, message.Attempts+1, delay))
	}
}

// whyNotDelivered strips the sentinel off a wrapped delivery error so the queue
// record carries the CAUSE in the operator's own words ("composer'da baska metin
// var...") rather than the English sentinel in front of it.
func whyNotDelivered(err error) string {
	reason := strings.TrimPrefix(err.Error(), bptmux.ErrNotReady.Error()+": ")
	if reason == "" {
		return bptmux.ErrNotReady.Error()
	}
	return reason
}

// stillHolds reports whether a composer is STILL showing this record's text —
// exact or damaged, both mean the message never went in.
func stillHolds(pane, message string) bool {
	_, ours := bptmux.StuckPaste(pane, []string{message})
	return ours
}

// finishHangingPaste presses Enter on this record's own unsubmitted paste, when
// the composer provably still holds it. Returns false for anything it may not
// touch — a forced record (see the interlock note at the call site), a pane that
// refuses, or a composer holding something else.
func (q *Queue) finishHangingPaste(ctx context.Context, target Target, rec record) (bool, error) {
	if rec.ForceBusy {
		return false, nil
	}
	return target.SubmitStuck(ctx, rec.To, []string{rec.Msg})
}

// settleUnrepasted handles a record that may never be pasted again: it waits for
// the transcript witness (which runs before this on every pass) and, when that
// wait runs out, closes the record honestly and tells the SENDER once.
//
// Telling the sender is the point. Both roads into this state end in "we do not
// know", and an unknown that nobody hears about is how a message goes missing in
// silence — the failure mode this queue keeps rediscovering.
//
// It reports whether the record was CLOSED, because a record still waiting keeps
// its target's line shut: nothing younger may overtake a message whose fate is
// undecided.
// noticeHomes maps a PLUMBING identity to the tmux session its notices can
// actually reach. The identities the force-busy allowlist accepts are labels,
// not sessions: the WhatsApp bridge signs as "whatsapp" (bridge.js pins
// AGENT=whatsapp) while its agent's session is "server-whatsapp" — so a notice
// queued to "whatsapp" targets a session that does not exist and is silently
// cancelled on the next pass as "target closed". An undeliverable notification
// is not a notification (server-whatsapp, 2026-08-21, who found this before the
// first notice was ever lost). The set is closed and small on purpose: it
// mirrors the allowlist, and ordinary agents sign with their session name and
// need no mapping.
var noticeHomes = map[string]string{
	"whatsapp": "server-whatsapp",
	"bp":       "server-main",
}

// noticeHome resolves where a sender's notices should go.
func noticeHome(from string) string {
	if home, ok := noticeHomes[from]; ok {
		return home
	}
	return from
}

func (q *Queue) settleUnrepasted(ctx context.Context, target Target, path string, message Message, report func(string)) bool {
	if q.Now().Sub(time.Unix(0, int64(message.TS*1e9))) < witnessWindow {
		return false
	}
	// Which of the two roads got here decides the wording: an injection nobody
	// could confirm may well have landed, while an attempt that never got past a
	// verdict of failure probably did not.
	status := "delivered (unverified)"
	if message.Reason == exhaustedReason {
		status = "not delivered (verification failed)"
	}
	if q.shouldNotify(message) {
		// Notified is persisted BEFORE the notice is queued. A crash in between
		// costs one missing notice; the other order would risk sending the same
		// notice on every later pass, and duplicated messages are the very bug
		// this change exists to end.
		message.Notified = true
		q.update(path, message, report)
		home := noticeHome(message.From)
		switch {
		case target != nil && !target.HasSession(ctx, home):
			// A notice queued to a session that does not exist would be silently
			// cancelled as "target closed" on the next pass — the notification
			// channel swallowing its own notifications. Say it LOUDLY instead of
			// queueing it into the void; the record's own closing line below still
			// carries the failed delivery.
			if report != nil {
				report(fmt.Sprintf("msgq: %s icin gonderene haber TESLIM EDILEMIYOR — %q icin oturum yok (from=%q); teslimat sonucu yalnizca bu log'da",
					message.ID, home, message.From))
			}
		default:
			if _, err := q.enqueueLocked(home, "bp", noticeText(message), enqueueOptions{}); err != nil {
				if report != nil {
					report(fmt.Sprintf("msgq: %s icin gonderene haber verilemedi: %v", message.ID, err))
				}
			} else if report != nil {
				report(fmt.Sprintf("msgq: %s teslimati dogrulanamadi; gonderen %s haberdar edildi (%s)", message.ID, message.From, home))
			}
		}
	}
	if err := q.finish(path, message, status); err != nil {
		if report != nil {
			report(fmt.Sprintf("msgq: could not finish %s: %v", message.ID, err))
		}
		return false
	}
	if report != nil {
		report(fmt.Sprintf("%s: %s -> %s (transcript tanigi %s icinde bulamadi); bak: bp peek %s",
			strings.ToUpper(status), message.ID, message.To, witnessWindow, message.To))
	}
	return true
}

// shouldNotify keeps the notice from becoming noise or a loop: it goes out once
// (Notified), only to a real sender, never from bp to itself, and never to a
// target that is its own sender — an agent that messaged itself would otherwise
// be told about its own message.
func (q *Queue) shouldNotify(message Message) bool {
	return !message.Notified && message.From != "" && message.From != "bp" && message.From != message.To
}

// noticeText is what the sender reads: which message, to whom, how to look, and
// enough of the opening to recognise it. It quotes the head only — a notice that
// repeated the whole message would be indistinguishable from a re-delivery.
func noticeText(message Message) string {
	head := []rune(strings.TrimSpace(message.Msg))
	if len(head) > noticeHeadRunes {
		head = head[:noticeHeadRunes]
	}
	return fmt.Sprintf("bp: %s mesajinin (%s hedefine) teslimati dogrulanamadi; bp peek %s ile kontrol et. Bas: %s",
		message.ID, message.To, message.To, string(head))
}

// writePending rewrites a pending record in place, atomically. The temp file is
// created without a .json suffix so a concurrent List/Dispatch glob cannot pick
// it up half-written.
func writePending(path string, message Message) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".reason-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0644); err != nil {
		_ = tmp.Close()
		return err
	}
	err = json.NewEncoder(tmp).Encode(message)
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (q *Queue) finish(path string, message Message, status string) error {
	if err := os.MkdirAll(q.done(), 0755); err != nil {
		return err
	}
	message.Status = status
	message.Finished = float64(q.Now().UnixNano()) / 1e9
	target := filepath.Join(q.done(), message.ID+".json")
	tmp, err := os.CreateTemp(q.done(), ".done-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	err = json.NewEncoder(tmp).Encode(message)
	if syncErr := tmp.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, target); err != nil {
		return err
	}
	return os.Remove(path)
}

// lineState is what one pass remembers about ONE target: its queue is a LINE, not
// a set, and both fields exist to keep it that way.
type lineState struct {
	// blockedBy is the id of the oldest record for this target that the pass could
	// not finish. While it is set, nothing younger for the same target is acted on
	// — that is the head-of-line rule.
	blockedBy string
	// delivered is the id of the record pasted into this target during THIS pass.
	// At most one paste per target per pass: the next record waits for a pass whose
	// own capture and witness can see the previous message leave the composer.
	delivered string
	// forceHeld is the id of a FORCE record whose paste reached the pane and could
	// not be verified, and forceHeldFrom the moment it did (Message.ForcedAt). They
	// are the only hold a later record may ever step over, and only a FORCE record
	// may do it, and only after forceCooldown — see forceReleased.
	forceHeld     string
	forceHeldFrom float64
}

// block records that this record is holding the line. The FIRST blocker wins, so
// every message behind it names the head rather than its immediate neighbour: the
// head is the one an operator has to do something about.
func (l *lineState) block(id string) {
	if l.blockedBy == "" {
		l.blockedBy = id
	}
}

// holdForce remembers that the record now holding this line is a FORCE record
// whose text may be sitting in the composer unsubmitted. Like block, the first
// one wins: the ceiling below is measured from the earliest such record, not from
// whichever one the walk saw last.
func (l *lineState) holdForce(id string, forcedAt float64) {
	if l.forceHeld == "" {
		l.forceHeld, l.forceHeldFrom = id, forcedAt
	}
}

// hold reports why this record may not be acted on yet, or "" when it is the head
// of a line that has not moved in this pass.
func (l *lineState) hold() string {
	if l.delivered != "" {
		return deliveredThisPassReason(l.delivered)
	}
	if l.blockedBy != "" {
		return headOfLineReason(l.blockedBy)
	}
	return ""
}

// forceReleased reports whether this FORCE record may stop waiting behind the
// forced message in front of it.
//
// The wait is the safety, not an accident: a follower normally waits until the
// transcript confirms that the message before it actually reached the agent,
// because two forced messages pasted into one TUI window back to back is the
// merge condition of 2026-08-17 itself. But the witness can stay silent, and a
// forced message is by definition one somebody is waiting for — so the wait has
// a ceiling of forceCooldown from the moment the earlier text reached the pane.
// The earlier record is not abandoned when the ceiling passes: it stays in the
// witness channel and closes on its own witnessWindow.
//
// Three things it deliberately does NOT release: a normal record (it is behind
// every force record by construction and stays there), a line that has already
// taken a paste in THIS pass, and a hold that comes from anything other than a
// force record's unverified paste — a pane lock, a foreign composer or a backoff
// means nothing went into the pane, so overtaking would only reorder messages.
func (q *Queue) forceReleased(rec record, line *lineState) bool {
	if !rec.ForceBusy || line.delivered != "" {
		return false
	}
	if line.forceHeld == "" || line.forceHeld != line.blockedBy || line.forceHeldFrom == 0 {
		return false
	}
	return !q.Now().Before(time.Unix(0, int64(line.forceHeldFrom*1e9)).Add(forceCooldown))
}

// paneLockReason turns a failed lock acquisition into the words a queue record
// carries. A contended lock is the ordinary case and has its own sentence; a
// broken one (the directory could not be created) is reported verbatim, because
// then bp is not waiting for anything — it is misconfigured.
func paneLockReason(err error) string {
	if errors.Is(err, bptmux.ErrPaneLocked) {
		return bptmux.BlockedByPaneLock
	}
	return err.Error()
}

// lockPane takes the cross-process pane lock for a target. The lock file lives
// under the queue root, which is the same directory the CLI resolves it from, so
// `bp msg`, `bp open`'s digest flush and this loop cannot type into one composer
// at the same time.
func (q *Queue) lockPane(session string) (func(), error) {
	return bptmux.AcquirePaneLock(q.Root, session)
}

// Dispatch makes one pass in send-time order. Busy or typed composers remain
// pending, and a target's queue is drained strictly in order: see dispatchRecord.
func (q *Queue) Dispatch(ctx context.Context, target Target, report func(string)) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := os.MkdirAll(q.Root, 0755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(q.Root, ".dispatch.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil
		}
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	records, bad, err := q.pendingRecords()
	if err != nil {
		return err
	}
	for _, broken := range bad {
		if report != nil {
			report(fmt.Sprintf("msgq: could not read %s: %v", broken.path, broken.err))
		}
	}
	// One line per target, walked target by target: a line is a queue and the
	// records in it are only comparable with each other.
	targets, byTarget := lines(records)
	for _, to := range targets {
		line := &lineState{}
		for _, rec := range byTarget[to] {
			q.dispatchRecord(ctx, target, rec, line, report)
		}
	}
	return q.Cleanup()
}

// lines groups a pass's records into one line per target — the targets in the
// order their oldest message was sent, so the pass keeps walking the fleet in
// send-time order — and puts each line in DELIVERY order.
//
// Delivery order inside a line is (force, send time, id). The force part is the
// only new thing and it is what --force-busy buys: a message someone deliberately
// put in front of the queue is delivered before the ordinary traffic waiting for
// the same agent, and forced messages keep send order among themselves, because
// two of them are usually one conversation.
func lines(records []record) ([]string, map[string][]record) {
	order := make([]string, 0, len(records))
	byTarget := make(map[string][]record, len(records))
	for _, rec := range records {
		if _, seen := byTarget[rec.To]; !seen {
			order = append(order, rec.To)
		}
		byTarget[rec.To] = append(byTarget[rec.To], rec)
	}
	for _, to := range order {
		line := byTarget[to]
		// A STABLE partition: the records arrive in (TS, id) order already, so
		// moving the force records to the front is the whole of the reordering and
		// send order survives inside both groups.
		sort.SliceStable(line, func(i, j int) bool { return line[i].ForceBusy && !line[j].ForceBusy })
	}
	return order, byTarget
}

// dispatchRecord decides what happens to ONE pending record in this pass.
//
// The head-of-line rule lives here, and it is what makes the queue a queue. Every
// record for one target sits in a line ordered by send time. The transcript
// WITNESS still runs for each of them — a witness is proof of a past delivery, not
// a new one, so letting it close a young record cannot reorder anything — but every
// ACTION that touches the pane (Send, the never-paste-again settlement, a backoff
// retry, erasing a delivered copy) is applied only to the record at the HEAD. While
// the head stays pending, the records behind it are skipped with a reason that
// names it.
//
// The cost is bounded and deliberate: a NoRepaste head can hold its line for up to
// witnessWindow (15 minutes) while the transcript is given its chance. In
// instruction traffic ORDER beats latency — the 2026-08-17 incident delivered a
// "DUR/IPTAL" correction after the instruction it cancelled, and fifteen minutes of
// silence would have been the cheaper failure by far.
func (q *Queue) dispatchRecord(ctx context.Context, target Target, rec record, line *lineState, report func(string)) {
	if !target.HasSession(ctx, rec.To) {
		// A closed target cancels the record without touching any pane, so it is
		// neither an action nor a reason to hold the line.
		if err := q.finish(rec.path, rec.Message, "canceled (target closed)"); err != nil && report != nil {
			report(fmt.Sprintf("msgq: could not finish %s: %v", rec.ID, err))
		}
		return
	}
	// The transcript is the witness, and it is asked FIRST — before the pane, before
	// the line. Whether the message already arrived has nothing to do with what the
	// composer looks like now or with whose turn it is to be delivered.
	witnessed := q.Witness != nil && q.Witness(rec.To, rec.Msg, time.Unix(0, int64(rec.TS*1e9)))
	if witnessed || rec.Cleanup {
		q.settleDelivered(ctx, target, rec, line, report)
		return
	}
	if held := line.hold(); held != "" && !q.forceReleased(rec, line) {
		q.remember(rec.path, rec.Message, held, report)
		return
	}
	// A record that may never be pasted again gets no further than this. The witness
	// above is its only way to a "delivered" close; everything below exists to put
	// text into a pane, and for this record that is precisely what must not happen.
	if rec.NoRepaste {
		// Before waiting the window out: is the text simply sitting in the
		// composer, never submitted? The witness above already said it is not in
		// the transcript, so if the box holds it EXACTLY then the delivery never
		// completed and pressing Enter finishes it — no second paste, same
		// message. Measured 2026-08-23: without this the paste hangs until a
		// human presses Enter (probot-outreach did, on ig-kuanta) or until
		// another message to the same target happens to resolve it.
		// FORCED records are excluded, deliberately. The follower-force interlock
		// (designed with ada, field-verified 2026-08-21) holds a second forced
		// message while the first one still sits in the composer; finishing that
		// first paste here would dissolve the state the interlock waits on. The
		// case measured today was an ordinary record, so the fix is scoped to
		// ordinary records — an interlock nobody has reported a problem with is
		// not something to redesign as a side effect.
		if submitted, err := q.finishHangingPaste(ctx, target, rec); err == nil && submitted {
			if err := q.finish(rec.path, rec.Message, "delivered (asili paste tamamlandi)"); err != nil && report != nil {
				report(fmt.Sprintf("msgq: could not finish %s: %v", rec.ID, err))
			}
			return
		} else if pane, err := target.CaptureAnsi(ctx, rec.To); err == nil && !rec.ForceBusy && stillHolds(pane, rec.Msg) {
			// bp could not finish it (a damaged box, a busy or asking pane, a
			// forced record) but the text IS still sitting there — so the record
			// must say what a PERSON should do. The old wording named the
			// mechanism ("waiting for the transcript witness"), which is true and
			// useless: the witness cannot settle a message that was never
			// submitted, and probot-outreach found three of these by looking at
			// panes rather than at bp (2026-08-23). A reason that suggests no
			// action is the silent kind of stuck.
			//
			// Forced records skip this too: their held composer is the interlock
			// doing its job, and it has its own ceiling below (holdForce). Two
			// tests caught me changing that path tonight; the interlock stays
			// exactly as ada and I built it.
			q.remember(rec.path, rec.Message, hangingPasteReason, report)
			line.block(rec.ID)
			return
		}
		if !q.settleUnrepasted(ctx, target, rec.path, rec.Message, report) {
			line.block(rec.ID)
			if rec.ForceBusy {
				// A forced message whose paste could not be confirmed holds the line
				// like any other, but with a ceiling: see forceReleased.
				line.holdForce(rec.ID, rec.ForcedAt)
			}
		}
		return
	}
	// From here on the pane is touched, so the pane belongs to this process alone:
	// capture, verify, paste, submit and verify are one critical section. Without it
	// another bp pasting into the same composer turns two messages into one (the
	// digest/instruction merge of 2026-08-17).
	release, err := q.lockPane(rec.To)
	if err != nil {
		q.remember(rec.path, rec.Message, paneLockReason(err), report)
		line.block(rec.ID)
		return
	}
	defer release()
	pane, captureErr := target.Capture(ctx, rec.To)
	paneAnsi, ansiErr := target.CaptureAnsi(ctx, rec.To)
	if captureErr != nil || ansiErr != nil {
		// Nothing is known about this pane, so nothing younger may pass it either.
		line.block(rec.ID)
		return
	}
	// Two gates, one verdict. The screen is asked first because it is free and
	// answers for every pane type; the transcript is asked only when the screen says
	// idle, and it is the one that sees a streaming turn. Both produce the SAME
	// record reason: from the queue's side there is no difference between the two
	// kinds of busy, and the reason a human reads should not depend on which gate
	// happened to catch it.
	//
	// A forced record is the one thing that walks past this, in BOTH its forms: the
	// spinner on the screen and the open turn only the transcript can see. That is
	// what the flag is for — a message from Tuna's phone must not sit out a tool
	// phase, which is most of a busy agent's time. It goes in through SendForce
	// below, so the delivery keeps the pane lock, the composer refusals and the
	// verification; what it gives up is the screen's ability to CONFIRM it, which
	// is why such a record usually ends up in the transcript witness's hands.
	// ...with ONE pane type excepted from the exception (2026-08-22): a busy
	// HERMES pane. There, submitting text mid-turn INTERRUPTS the turn instead of
	// queueing behind it — the pane says so itself, drawing
	// "⚕ ❯ msg=interrupt · /queue · …" while it works (measured in
	// blueprint-hermes-test). A forced record would therefore not jump the queue,
	// it would cancel the work the sender wanted to reach. Client.SendForce
	// refuses this too and is the real guarantee; the check is repeated here so the
	// record gets the honest "pane calisiyor" reason a human can read in `bp q`
	// instead of a delivery error.
	if (!rec.ForceBusy || bptmux.HermesPane(pane)) && (bptmux.Busy(pane) || (q.TurnOpen != nil && q.TurnOpen(rec.To))) {
		q.remember(rec.path, rec.Message, bptmux.BlockedByBusyPane, report)
		line.block(rec.ID)
		return
	}
	// A non-empty composer used to end the attempt right here, and that is how the
	// queue blocked itself: our OWN unsubmitted paste of this very message looked
	// exactly like a busy human. Now the pane is asked WHY it cannot take the
	// message, and an empty answer means Send may proceed — including when the
	// composer holds text provably ours, which Send knows how to finish. Every other
	// answer is recorded on the queue record, so a message that waits does not wait
	// silently.
	// A forced record asks the composer-only question: it overrides the working
	// agent, never the contents of the box. Everything the ordinary gate refuses
	// for — a paste chip, somebody's half-written line — refuses it too.
	blocked := bptmux.ComposerBlockReason
	if rec.ForceBusy {
		blocked = bptmux.ComposerContentBlockReason
	}
	if reason := blocked(paneAnsi, []string{rec.Msg}); reason != "" {
		q.remember(rec.path, rec.Message, reason, report)
		line.block(rec.ID)
		return
	}
	// Backing off after a proven failure. The witness above already ran, so a
	// message that did arrive still closes on time; only the PASTE waits.
	if rec.NextTry > 0 && q.Now().Before(time.Unix(0, int64(rec.NextTry*1e9))) {
		line.block(rec.ID)
		return
	}
	// The pane is free: drop any stale reason before handing over to Send.
	q.remember(rec.path, rec.Message, "", report)
	message := rec.Message
	// Forced records go in through the door that may type into a working pane.
	// Everything else about the delivery is identical — same lock, same
	// verification, same error contract.
	deliver := target.Send
	if message.ForceBusy {
		deliver = target.SendForce
	}
	err = deliver(ctx, rec.To, message.Msg)
	// The moment a forced text reached the pane, recorded whatever the verdict on it
	// was: delivered, unconfirmed, or provably broken all mean keystrokes went in,
	// and it is the keystrokes the record behind this one has to wait out. The
	// refusals (a working pane, someone typing, a shell) injected nothing and start
	// no clock — a zero ForcedAt is what stops forceReleased from ever letting a
	// follower through on a record that never touched the composer.
	if message.ForceBusy && (err == nil || errors.Is(err, bptmux.ErrUnverified) || errors.Is(err, bptmux.ErrNotReady)) {
		message.ForcedAt = float64(q.Now().UnixNano()) / 1e9
	}
	if err != nil {
		line.block(rec.ID)
		if errors.Is(err, bptmux.ErrNotAgent) {
			// The target dropped to a shell: leave the message PENDING (never lose
			// it, never type into the shell) and report the skip. A later pass
			// delivers it if the target becomes an agent again.
			if report != nil {
				report(fmt.Sprintf("msgq: %s -> %s skipped: target not an agent", message.ID, message.To))
			}
			return
		}
		if errors.Is(err, bptmux.ErrTyping) {
			return
		}
		if errors.Is(err, bptmux.ErrBusy) {
			// The pane started a turn between the capture above and the paste.
			// Nothing was injected — this is the ordinary busy wait, recorded under
			// the same reason the capture would have given.
			q.remember(rec.path, message, bptmux.BlockedByBusyPane, report)
			return
		}
		if errors.Is(err, bptmux.ErrNotReady) {
			// PROVEN non-delivery (expired login, foreign composer): the message
			// stays PENDING, but not unconditionally and not forever.
			q.retryLater(rec.path, message, err, report)
			return
		}
		if errors.Is(err, bptmux.ErrUnverified) {
			// Injected and submitted, but nothing confirmed it either way. Closing
			// the record here — the old behavior — is honest only when nobody could
			// ever settle it: if the text never landed the message is silently LOST,
			// and if it did land the transcript witness would have closed the record
			// by itself. So whenever the witness could recognise this text, the
			// record is KEPT OPEN and marked never-paste-again: no copy can come out
			// of it, and the only remaining source of truth gets its chance.
			//
			// It also counts as a DELIVERY for this pass: something went into that
			// composer, and nothing else may follow it until a later pass has seen
			// the composer clear.
			line.delivered = message.ID
			if q.CanWitness != nil && q.CanWitness(message.Msg) {
				message.NoRepaste, message.Reason, message.NextTry = true, unverifiedReason, 0
				q.update(rec.path, message, report)
				if report != nil {
					report(fmt.Sprintf("delivery UNVERIFIED: %s -> %s; tekrar paste edilmeyecek, transcript tanigi bekleniyor", message.ID, message.To))
				}
				return
			}
			// Too short for the witness to identify (a slash command): nothing will
			// ever settle it, so waiting would only mean waiting forever.
			if err := q.finish(rec.path, message, "delivered (unverified)"); err != nil && report != nil {
				report(fmt.Sprintf("msgq: could not finish %s: %v", message.ID, err))
			}
			if report != nil {
				report(fmt.Sprintf("delivered (UNVERIFIED): %s -> %s; check with bp peek %s", message.ID, message.To, message.To))
			}
			return
		}
		if report != nil {
			report(fmt.Sprintf("msgq: could not send %s: %v", message.ID, err))
		}
		return
	}
	// Delivered. The line is closed for the rest of this pass even though this
	// record is gone: the next message must be pasted by a pass whose own capture
	// and witness have seen this one leave the composer. Two pastes in one pass is
	// how one agent was handed three messages inside a single second.
	line.delivered = message.ID
	if err := q.finish(rec.path, message, "delivered"); err != nil {
		if report != nil {
			report(fmt.Sprintf("msgq: could not finish %s: %v", message.ID, err))
		}
		return
	}
	if report != nil {
		report(fmt.Sprintf("delivered: %s -> %s", message.ID, message.To))
	}
}

// settleDelivered closes a record the transcript has PROVEN delivered — but not
// before the composer is clean.
//
// The order of those two steps is the fix. It used to close the record first and
// then ask the pane to drop the copy that may still be hanging in the composer from
// a paste whose Enter never registered; when that clearing could not run — a pane
// that had begun a turn answers ErrBusy — the record was already gone, so nothing
// could recognise the text as ours any more, and the agent's next turn submitted
// it: a byte-identical duplicate (op-main, 2026-08-17, the same 441 characters at
// 13:03:37 and 13:07:27). So the copy is dealt with FIRST. A record whose copy
// could not be cleared stays PENDING under the Cleanup flag — never pasted again,
// retried every pass, holding its line — and the witnessWindow ceiling closes it if
// the pane never frees up, because a record must not wait forever either.
func (q *Queue) settleDelivered(ctx context.Context, target Target, rec record, line *lineState, report func(string)) {
	if line.delivered != "" {
		// A paste went into this composer earlier in this pass. Pressing C-u on it
		// now could erase that message instead of the delivered copy, so the
		// cleanup — and with it the close — waits for the next pass.
		q.remember(rec.path, rec.Message, deliveredThisPassReason(line.delivered), report)
		line.block(rec.ID)
		return
	}
	release, err := q.lockPane(rec.To)
	if err != nil {
		q.remember(rec.path, rec.Message, paneLockReason(err), report)
		line.block(rec.ID)
		return
	}
	cleared, clearErr := target.ClearDelivered(ctx, rec.To, []string{rec.Msg})
	release()
	if clearErr == nil {
		// Either the hanging copy was erased, or there was nothing of ours in the
		// composer at all. Both mean the record can be closed without leaving an
		// unattributable copy behind.
		if err := q.finish(rec.path, rec.Message, "delivered (found in transcript)"); err != nil {
			if report != nil {
				report(fmt.Sprintf("msgq: could not finish %s: %v", rec.ID, err))
			}
			line.block(rec.ID)
			return
		}
		if report != nil {
			report(fmt.Sprintf("delivered earlier: %s -> %s (transcript'te bulundu, tekrar yazilmadi)", rec.ID, rec.To))
			if cleared {
				report(fmt.Sprintf("msgq: %s composer'inda asili duran teslim edilmis metin C-u ile temizlendi (%s)", rec.To, rec.ID))
			}
		}
		return
	}
	// The copy could not be cleared, and the pane would not even say what it is
	// holding (ErrBusy is answered before the composer is read). Assume the worst —
	// a copy of a delivered message sitting in the composer — and keep the record,
	// which is the only thing that can still identify that text as ours.
	if q.Now().Sub(time.Unix(0, int64(rec.TS*1e9))) >= witnessWindow {
		if err := q.finish(rec.path, rec.Message, "delivered (found in transcript)"); err != nil {
			if report != nil {
				report(fmt.Sprintf("msgq: could not finish %s: %v", rec.ID, err))
			}
			line.block(rec.ID)
			return
		}
		if report != nil {
			report(fmt.Sprintf("msgq: %s teslim edildi ama composer'daki kopya %s icinde temizlenemedi (%v); elle bak: bp peek %s",
				rec.ID, witnessWindow, clearErr, rec.To))
		}
		return
	}
	message := rec.Message
	message.Cleanup, message.NextTry = true, 0
	message.Reason = cleanupReason
	q.update(rec.path, message, report)
	if !rec.Cleanup && report != nil {
		// Said once, when the state is entered: every later pass repeats the attempt
		// silently until it works or the ceiling closes the record.
		report(fmt.Sprintf("msgq: %s teslim edildi (transcript) ama %s composer'indaki kopya temizlenemedi (%v); kayit temizlik icin acik tutuluyor",
			rec.ID, rec.To, clearErr))
	}
	line.block(rec.ID)
}

func (q *Queue) Cleanup() error {
	paths, err := filepath.Glob(filepath.Join(q.done(), "q*.json"))
	if err != nil {
		return err
	}
	cutoff := q.Now().Add(-48 * time.Hour)
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
	return nil
}

func (q *Queue) Run(ctx context.Context, interval time.Duration, target Target, report func(string)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	work := make(chan struct{}, 1)
	work <- struct{}{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			select {
			case work <- struct{}{}:
			default:
			}
		case <-work:
			if err := q.Dispatch(ctx, target, report); err != nil && report != nil {
				report("msgq: " + err.Error())
			}
		}
	}
}
