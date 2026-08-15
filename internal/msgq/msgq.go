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
	unverifiedReason = "teslimat belirsiz: ekran dogrulayamadi; transcript tanigi bekleniyor"
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
)

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
	return q.enqueueLocked(to, from, text, reason, false)
}

// EnqueueUnverified records a message that was ALREADY injected into the pane
// but could not be verified. The record exists only so the transcript witness
// can settle it: it is marked NoRepaste, so no pass will ever paste the text a
// second time. Without it an unverified send has no record at all — it is either
// silently lost or silently delivered, and nobody ever learns which.
func (q *Queue) EnqueueUnverified(to, from, text string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.enqueueLocked(to, from, text, unverifiedReason, true)
}

// enqueueLocked is the body of every enqueue, WITHOUT taking q.mu. It exists
// because Dispatch already holds the mutex and must be able to queue a message
// of its own (the notice to a sender whose delivery could not be verified);
// calling the exported entry point from there would deadlock against itself.
func (q *Queue) enqueueLocked(to, from, text, reason string, noRepaste bool) (string, error) {
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
	message := Message{ID: base, To: to, From: from, Msg: text, TS: float64(now.UnixNano()) / 1e9, Reason: reason, NoRepaste: noRepaste}
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

func (q *Queue) List() ([]Message, error) {
	paths, err := filepath.Glob(filepath.Join(q.pending(), "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	messages := make([]Message, 0, len(paths))
	for _, path := range paths {
		message, readErr := read(path)
		if readErr != nil {
			return nil, fmt.Errorf("%s: %w", path, readErr)
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func (q *Queue) Status(id string) (string, error) {
	if message, err := read(filepath.Join(q.pending(), id+".json")); err == nil {
		seconds := int(q.Now().Sub(time.Unix(0, int64(message.TS*1e9))).Seconds())
		if seconds < 0 {
			seconds = 0
		}
		// A record nobody will paste again must SAY so. Reporting it as an
		// ordinary "PENDING" would suggest the queue is still trying, and the
		// operator would keep waiting for a delivery that is now in the
		// transcript's hands.
		if message.NoRepaste {
			return fmt.Sprintf("PENDING (yeniden paste edilmeyecek): %s — %s (%d seconds queued); bak: bp peek %s",
				message.To, message.Reason, seconds, message.To), nil
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
	// ClearDelivered erases a composer that provably holds one of texts, and
	// nothing else. Dispatch calls it in exactly one situation: the transcript
	// proved a message already arrived, and the same text is STILL hanging in the
	// composer. Once the record is closed nothing can recognise that text as ours
	// again, so leaving it would block the target permanently — the deadlock, back
	// from the other end.
	ClearDelivered(context.Context, string, []string) (bool, error)
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
	paths, err := filepath.Glob(filepath.Join(q.pending(), "*.json"))
	if err != nil {
		return nil
	}
	sort.Strings(paths)
	var texts []string
	for _, path := range paths {
		if message, readErr := read(path); readErr == nil && message.To == to {
			texts = append(texts, message.Msg)
		}
	}
	return texts
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
	paths, err := filepath.Glob(filepath.Join(q.pending(), "*.json"))
	if err != nil {
		return "", false
	}
	sort.Strings(paths)
	for _, path := range paths {
		message, readErr := read(path)
		if readErr != nil || message.To != to || message.Msg != text {
			continue
		}
		if err := q.finish(path, message, status); err != nil {
			return "", false
		}
		return message.ID, true
	}
	return "", false
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

// settleUnrepasted handles a record that may never be pasted again: it waits for
// the transcript witness (which runs before this on every pass) and, when that
// wait runs out, closes the record honestly and tells the SENDER once.
//
// Telling the sender is the point. Both roads into this state end in "we do not
// know", and an unknown that nobody hears about is how a message goes missing in
// silence — the failure mode this queue keeps rediscovering.
func (q *Queue) settleUnrepasted(path string, message Message, report func(string)) {
	if q.Now().Sub(time.Unix(0, int64(message.TS*1e9))) < witnessWindow {
		return
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
		if _, err := q.enqueueLocked(message.From, "bp", noticeText(message), "", false); err != nil {
			if report != nil {
				report(fmt.Sprintf("msgq: %s icin gonderene haber verilemedi: %v", message.ID, err))
			}
		} else if report != nil {
			report(fmt.Sprintf("msgq: %s teslimati dogrulanamadi; gonderen %s haberdar edildi", message.ID, message.From))
		}
	}
	if err := q.finish(path, message, status); err != nil {
		if report != nil {
			report(fmt.Sprintf("msgq: could not finish %s: %v", message.ID, err))
		}
		return
	}
	if report != nil {
		report(fmt.Sprintf("%s: %s -> %s (transcript tanigi %s icinde bulamadi); bak: bp peek %s",
			strings.ToUpper(status), message.ID, message.To, witnessWindow, message.To))
	}
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

// Dispatch makes one sorted pass. Busy or typed composers remain pending.
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
	paths, err := filepath.Glob(filepath.Join(q.pending(), "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, path := range paths {
		message, readErr := read(path)
		if readErr != nil {
			if report != nil {
				report(fmt.Sprintf("msgq: could not read %s: %v", path, readErr))
			}
			continue
		}
		if !target.HasSession(ctx, message.To) {
			if err := q.finish(path, message, "canceled (target closed)"); err != nil {
				if report != nil {
					report(fmt.Sprintf("msgq: could not finish %s: %v", message.ID, err))
				}
			}
			continue
		}
		pane, captureErr := target.Capture(ctx, message.To)
		paneAnsi, ansiErr := target.CaptureAnsi(ctx, message.To)
		if captureErr != nil || ansiErr != nil {
			continue
		}
		// The transcript is the witness, and it is asked FIRST — before the pane is
		// allowed to block anything. Whether the message already arrived has nothing
		// to do with what the composer looks like now, and a target whose composer
		// is stuck must still be able to close records for messages it did receive.
		if q.Witness != nil && q.Witness(message.To, message.Msg, time.Unix(0, int64(message.TS*1e9))) {
			if err := q.finish(path, message, "delivered (found in transcript)"); err != nil {
				if report != nil {
					report(fmt.Sprintf("msgq: could not finish %s: %v", message.ID, err))
				}
				continue
			}
			if report != nil {
				report(fmt.Sprintf("delivered earlier: %s -> %s (transcript'te bulundu, tekrar yazilmadi)", message.ID, message.To))
			}
			// Two proofs meeting: the transcript says this text was delivered, and
			// the composer may still be holding it from the paste whose Enter never
			// registered. Closing the record destroys the only evidence that the
			// hanging text is ours, so it would read as foreign from now on and block
			// the target forever. Clear it here, while the proof is still in hand —
			// and say so, because bp erasing a composer is never a silent act.
			cleared, clearErr := target.ClearDelivered(ctx, message.To, []string{message.Msg})
			if report != nil {
				switch {
				case cleared:
					report(fmt.Sprintf("msgq: %s composer'inda asili duran teslim edilmis metin C-u ile temizlendi (%s)", message.To, message.ID))
				case clearErr != nil:
					report(fmt.Sprintf("msgq: %s composer'i temizlenemedi (%v); elle bak: bp peek %s", message.To, clearErr, message.To))
				}
			}
			continue
		}
		// A record that may never be pasted again gets no further than this. The
		// witness above is its only way to a "delivered" close; everything below
		// this line exists to put text into a pane, and for this record that is
		// precisely what must not happen.
		if message.NoRepaste {
			q.settleUnrepasted(path, message, report)
			continue
		}
		// Two gates, one verdict. The screen is asked first because it is free and
		// answers for every pane type; the transcript is asked only when the screen
		// says idle, and it is the one that sees a streaming turn. Both produce the
		// SAME record reason: from the queue's side there is no difference between
		// the two kinds of busy, and the reason a human reads should not depend on
		// which gate happened to catch it.
		if bptmux.Busy(pane) || (q.TurnOpen != nil && q.TurnOpen(message.To)) {
			q.remember(path, message, bptmux.BlockedByBusyPane, report)
			continue
		}
		// A non-empty composer used to end the attempt right here, and that is how
		// the queue blocked itself: our OWN unsubmitted paste of this very message
		// looked exactly like a busy human. Now the pane is asked WHY it cannot take
		// the message, and an empty answer means Send may proceed — including when
		// the composer holds text provably ours, which Send knows how to finish.
		// Every other answer is recorded on the queue record, so a message that
		// waits does not wait silently.
		if reason := bptmux.ComposerBlockReason(paneAnsi, []string{message.Msg}); reason != "" {
			q.remember(path, message, reason, report)
			continue
		}
		// Backing off after a proven failure. The witness above already ran, so a
		// message that did arrive still closes on time; only the PASTE waits.
		if message.NextTry > 0 && q.Now().Before(time.Unix(0, int64(message.NextTry*1e9))) {
			continue
		}
		// The pane is free: drop any stale reason before handing over to Send.
		q.remember(path, message, "", report)
		if err := target.Send(ctx, message.To, message.Msg); err != nil {
			if errors.Is(err, bptmux.ErrNotAgent) {
				// The target dropped to a shell: leave the message PENDING (never
				// lose it, never type into the shell) and report the skip. A later
				// pass delivers it if the target becomes an agent again.
				if report != nil {
					report(fmt.Sprintf("msgq: %s -> %s skipped: target not an agent", message.ID, message.To))
				}
				continue
			}
			if errors.Is(err, bptmux.ErrTyping) {
				continue
			}
			if errors.Is(err, bptmux.ErrBusy) {
				// The pane started a turn between the capture above and the
				// paste. Nothing was injected — this is the ordinary busy wait,
				// recorded under the same reason the capture would have given.
				q.remember(path, message, bptmux.BlockedByBusyPane, report)
				continue
			}
			if errors.Is(err, bptmux.ErrNotReady) {
				// PROVEN non-delivery (expired login, foreign composer): the
				// message stays PENDING, but not unconditionally and not forever.
				q.retryLater(path, message, err, report)
				continue
			}
			if errors.Is(err, bptmux.ErrUnverified) {
				// Injected and submitted, but nothing confirmed it either way.
				// Closing the record here — the old behavior — is honest only
				// when nobody could ever settle it: if the text never landed the
				// message is silently LOST, and if it did land the transcript
				// witness would have closed the record by itself. So whenever the
				// witness could recognise this text, the record is KEPT OPEN and
				// marked never-paste-again: no copy can come out of it, and the
				// only remaining source of truth gets its chance.
				if q.CanWitness != nil && q.CanWitness(message.Msg) {
					message.NoRepaste, message.Reason, message.NextTry = true, unverifiedReason, 0
					q.update(path, message, report)
					if report != nil {
						report(fmt.Sprintf("delivery UNVERIFIED: %s -> %s; tekrar paste edilmeyecek, transcript tanigi bekleniyor", message.ID, message.To))
					}
					continue
				}
				// Too short for the witness to identify (a slash command): nothing
				// will ever settle it, so waiting would only mean waiting forever.
				if err := q.finish(path, message, "delivered (unverified)"); err != nil && report != nil {
					report(fmt.Sprintf("msgq: could not finish %s: %v", message.ID, err))
				}
				if report != nil {
					report(fmt.Sprintf("delivered (UNVERIFIED): %s -> %s; check with bp peek %s", message.ID, message.To, message.To))
				}
				continue
			}
			if report != nil {
				report(fmt.Sprintf("msgq: could not send %s: %v", message.ID, err))
			}
			continue
		}
		if err := q.finish(path, message, "delivered"); err != nil {
			if report != nil {
				report(fmt.Sprintf("msgq: could not finish %s: %v", message.ID, err))
			}
			continue
		}
		if report != nil {
			report(fmt.Sprintf("delivered: %s -> %s", message.ID, message.To))
		}
	}
	return q.Cleanup()
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
