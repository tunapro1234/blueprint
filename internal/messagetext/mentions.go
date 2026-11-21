package messagetext

import (
	"strings"
	"unicode"
)

// TrailingMention returns the final token of text when Claude Code would read
// it as an @-mention still being typed: the text ends in a token that starts
// with "@" at the start of the text or after whitespace. It returns "" for
// anything else, including text that already ends in whitespace.
//
// Claude Code 2.1.330 opens its mention picker on such a token after a paste
// (files in the working directory and "message session" entries for other
// Claude sessions on the machine), and the Enter that should submit the
// message accepts the highlighted suggestion instead. Measured 2026-11-21
// (q915605609 -> server-necati-vbaipr): "... @pr" became "... @probot-egitim"
// and stayed unsent in the composer. An "@" inside a token ("a@b", "(@pr",
// "`@pr`") does not open the picker and is not reported.
func TrailingMention(text string) string {
	if text == "" || strings.TrimRightFunc(text, unicode.IsSpace) != text {
		return ""
	}
	start := strings.LastIndexFunc(text, unicode.IsSpace) + 1
	token := text[start:]
	if !strings.HasPrefix(token, "@") {
		return ""
	}
	return token
}

// NeutralizeTrailingMention appends one space to text whose final token would
// open Claude Code's mention picker (see TrailingMention), so the paste ends
// outside the token and Enter submits the message. Measured on Claude Code
// 2.1.330: with the trailing space the picker stays closed for "@pr", "@" and
// `@"pr`, Enter submits, and the transcript stores the text without the
// trailing space. Every other text is returned unchanged, so the function is
// idempotent.
//
// The space is invisible to every comparison bp makes: the composer checks
// strip whitespace and the transcript witness trims it. A record queued before
// this rule therefore still matches its own paste, its screen and its
// transcript entry.
func NeutralizeTrailingMention(text string) (string, bool) {
	if TrailingMention(text) == "" {
		return text, false
	}
	return text + " ", true
}

// NeutralizeForClaude applies every intake rewrite that keeps Claude Code from
// turning pasted message text into something else: image paths become text
// (NeutralizeImagePaths) and a trailing @-mention stays plain text
// (NeutralizeTrailingMention). It is deterministic and idempotent, so the
// queue, the CLI and a restarted daemon all derive the same text.
func NeutralizeForClaude(text string) string {
	text, _ = NeutralizeImagePaths(text)
	text, _ = NeutralizeTrailingMention(text)
	return text
}
