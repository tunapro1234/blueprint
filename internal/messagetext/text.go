// Package messagetext keeps message data from becoming terminal input commands.
package messagetext

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrUnsafe = errors.New("unsafe message text")

// Validate rejects rather than rewrites: a rewritten message would differ from
// its queue/transcript witness. LF and TAB are permitted inside bracketed paste.
// In particular, an embedded ESC [201~ must never close that paste early.
func Validate(texts ...string) error {
	for _, text := range texts {
		if !utf8.ValidString(text) {
			return fmt.Errorf("%w: invalid UTF-8", ErrUnsafe)
		}
		for _, r := range text {
			bidi := r == '\u061c' || r == '\u200e' || r == '\u200f' || (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
			if (unicode.IsControl(r) && r != '\n' && r != '\t') || bidi {
				// Never echo the offending bytes back into the operator's terminal.
				return fmt.Errorf("%w: U+%04X; use a printable escape notation instead", ErrUnsafe, r)
			}
		}
	}
	return nil
}

// Label is one envelope field, never additional lines or closing brackets.
// This validates representation; it does not authenticate the sender.
func Label(label string) error {
	if err := Validate(label); err != nil {
		return err
	}
	if strings.ContainsAny(label, "[]\n\t\u2028\u2029") {
		return fmt.Errorf("%w: sender label contains envelope delimiters", ErrUnsafe)
	}
	return nil
}

// Sender requires a usable origin label, including when replaying legacy storage.
// A readable uncertain label is allowed; this does not confer authority.
func Sender(label string) error {
	if err := Label(label); err != nil {
		return err
	}
	if value := strings.TrimSpace(label); value == "" || value == "bilinmiyor" {
		return errors.New("sender identity unavailable; anonymous delivery blocked")
	}
	return nil
}
