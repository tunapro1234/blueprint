package messagetext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type claudePiece struct {
	start int
	end   int
}

// NeutralizeImagePaths wraps pieces that Claude Code would treat as image
// attachments in backticks. It mirrors the split Claude applies to pasted
// multi-character input, and preserves every other byte of text.
func NeutralizeImagePaths(text string) (string, int) {
	pieces := claudeImagePieces(text)
	var images []claudePiece
	for _, piece := range pieces {
		if !isClaudeImage(text[piece.start:piece.end]) {
			continue
		}
		part := text[piece.start:piece.end]
		left := len(part) - len(strings.TrimLeftFunc(part, isClaudeSpace))
		right := len(part) - len(strings.TrimRightFunc(part, isClaudeSpace))
		images = append(images, claudePiece{start: piece.start + left, end: piece.end - right})
	}
	if len(images) == 0 {
		return text, 0
	}
	var out strings.Builder
	out.Grow(len(text) + 2*len(images))
	previous := 0
	for _, image := range images {
		out.WriteString(text[previous:image.start])
		out.WriteByte('`')
		out.WriteString(text[image.start:image.end])
		out.WriteByte('`')
		previous = image.end
	}
	out.WriteString(text[previous:])
	return out.String(), len(images)
}

// ClaudeImageTransform returns the text Claude Code leaves after extracting
// image pieces from pasted input. The boolean is true when at least one piece
// is extracted. This also supplies the transcript witness with the alternate
// text shape produced by older, unneutralized queued messages.
func ClaudeImageTransform(text string) (string, bool) {
	pieces := claudeImagePieces(text)
	var rest []string
	foundImage := false
	for _, piece := range pieces {
		part := text[piece.start:piece.end]
		if isClaudeImage(part) {
			foundImage = true
			continue
		}
		rest = append(rest, part)
	}
	if !foundImage {
		return text, false
	}
	return strings.Join(rest, "\n"), true
}

// claudeImagePieces follows Z.split(/ (?=\/|[A-Za-z]:\\)/), then splits
// each result on LF and drops whitespace-only pieces. The separating space and
// LF stay in the original string; callers use byte spans to preserve them.
func claudeImagePieces(text string) []claudePiece {
	var pieces []claudePiece
	start := 0
	appendPiece := func(end int) {
		if claudeTrimSpace(text[start:end]) != "" {
			pieces = append(pieces, claudePiece{start: start, end: end})
		}
	}
	for i := 0; i < len(text); {
		if text[i] == '\n' {
			appendPiece(i)
			i++
			start = i
			continue
		}
		if text[i] == ' ' && startsClaudePath(text, i+1) {
			appendPiece(i)
			i++ // Claude's split consumes the space, but leaves the path prefix.
			start = i
			continue
		}
		_, size := utf8.DecodeRuneInString(text[i:])
		if size < 1 {
			size = 1
		}
		i += size
	}
	appendPiece(len(text))
	return pieces
}

func startsClaudePath(text string, i int) bool {
	if i >= len(text) {
		return false
	}
	if text[i] == '/' {
		return true
	}
	return i+2 < len(text) && isASCIILetter(text[i]) && text[i+1] == ':' && text[i+2] == '\\'
}

func isASCIILetter(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func isClaudeImage(piece string) bool {
	value := claudeTrimSpace(piece)
	if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	value = unescapeClaudeBackslashes(value)
	value = strings.ToLower(value)
	for _, suffix := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp"} {
		if strings.HasSuffix(value, suffix) {
			return true
		}
	}
	return false
}

func claudeTrimSpace(value string) string {
	return strings.TrimFunc(value, isClaudeSpace)
}

func isClaudeSpace(r rune) bool {
	// ECMAScript String.trim includes FEFF in addition to Unicode White_Space.
	return unicode.IsSpace(r) || r == '\ufeff'
}

// Claude removes one escaping backslash before the following character. Two
// backslashes therefore become one, matching its string-unescape behavior.
func unescapeClaudeBackslashes(value string) string {
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); {
		if value[i] == '\\' && i+1 < len(value) {
			i++
		}
		_, size := utf8.DecodeRuneInString(value[i:])
		if size < 1 {
			size = 1
		}
		out.WriteString(value[i : i+size])
		i += size
	}
	return out.String()
}
