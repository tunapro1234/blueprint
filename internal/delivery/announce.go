package delivery

import (
	"strings"

	"blueprint/internal/msgq"
	"blueprint/internal/pending"
)

// AnnouncePrefix is the envelope bp announce puts in front of the text.
func AnnouncePrefix(sender string) string { return "[ANNOUNCE " + sender + "] " }

// AnnounceDeferrer returns the msgq.Queue.DeferCold hook: when a spread
// announcement's slot comes and warm says its target is no longer warm (or is
// closed), the announcement is appended to the target's offline spool, exactly
// as bp announce spools a cold target, and rides its next natural turn. The
// spool entry keeps the time the announcement was sent.
func AnnounceDeferrer(stateDir string, warm func(string) bool) func(msgq.Message) (bool, error) {
	return func(m msgq.Message) (bool, error) {
		if warm(m.To) {
			return false, nil
		}
		text := strings.TrimPrefix(m.Msg, AnnouncePrefix(m.From))
		entry := pending.Entry{TS: int64(m.TS), From: m.From, Kind: "announce", Text: text}
		if err := pending.Append(stateDir, m.To, entry); err != nil {
			return false, err
		}
		return true, nil
	}
}
