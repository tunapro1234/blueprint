package guard

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind groups findings for the audit log and the frame header.
type Kind string

const (
	KindOverride   Kind = "instruction-override"
	KindSpoof      Kind = "envelope-spoof"
	KindTool       Kind = "tool-request"
	KindCredential Kind = "credential-request"
	KindExfil      Kind = "exfil-url"
	KindHidden     Kind = "hidden-unicode"
	KindEncoded    Kind = "encoded-payload"
	KindSecret     Kind = "secret"
)

// Severity orders findings. High findings should alert the owner.
type Severity string

const (
	Info Severity = "info"
	Warn Severity = "warn"
	High Severity = "high"
)

// Finding is one flagged span. Excerpt is escaped, secret-redacted and short,
// safe to print in a terminal or store in the audit log.
type Finding struct {
	Kind     Kind     `json:"kind"`
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	Offset   int      `json:"offset"`
	Excerpt  string   `json:"excerpt,omitempty"`
	// Decoded is set when the finding was inside a decoded payload.
	Decoded bool `json:"decoded,omitempty"`
}

type rule struct {
	kind     Kind
	name     string
	severity Severity
	re       *regexp.Regexp
}

func r(kind Kind, name string, sev Severity, expr string) rule {
	return rule{kind, name, sev, regexp.MustCompile(expr)}
}

// Rules are deliberately broad: a finding flags and audits, it never blocks,
// so a false positive costs one log line.
var rules = []rule{
	r(KindOverride, "ignore-previous", High, `(?i)\b(ignore|disregard|forget|override|bypass)\b[^.\n]{0,40}\b(previous|prior|above|earlier|all|your|system|safety|owner'?s?)\b[^.\n]{0,20}\b(instructions?|prompts?|rules?|guidelines?|directives?|messages?)`),
	r(KindOverride, "new-role", Warn, `(?i)\b(you are now|from now on,? you|act as (an? )?(unrestricted|admin|root|system)|developer mode|jailbreak|DAN mode)\b`),
	r(KindOverride, "system-prompt", Warn, `(?i)\b(new|updated|real|actual|hidden) (system )?(instructions?|system prompt)\b|\bsystem prompt\b`),
	r(KindOverride, "owner-claim", High, `(?i)\b(this is|message from|i am|i'm) (your|the) (owner|user|admin|operator|coordinator)\b|\b(your|the) owner (says|said|wants|asked|approved|authori[sz]ed)\b`),
	r(KindOverride, "ignore-previous-tr", High, `(?i)(önceki|yukarıdaki|tüm|bütün) (talimat|komut|kural)[a-zçğıöşü]*\s+(yok say|unut|görmezden gel|geçersiz)`),
	r(KindOverride, "do-not-tell", High, `(?i)\b(do not|don't|never) (tell|inform|notify|alert|mention (this )?to) (your |the )?(owner|user|human|operator)\b|\bkeep (this|it) (secret|hidden) from\b`),

	r(KindSpoof, "envelope-line", High, `(?m)^[ \t]*\[[^\]\n]{1,80}\][ \t]`),
	r(KindSpoof, "frame-marker", High, `(?i)<<<\s*(end\s+)?bp-`),
	r(KindSpoof, "chat-role-tag", Warn, `(?i)</?\s*(system|assistant|user|untrusted|instructions?|tool_result|function_results)\s*>|(?m)^\s*(system|assistant|human)\s*:`),

	r(KindTool, "shell-command", Warn, `(?i)\b(run|execute|exec|type|paste|eval)\b[^.\n]{0,30}\b(command|script|shell|bash|terminal|this|following)\b`),
	r(KindTool, "pipe-to-shell", High, `(?i)\b(curl|wget|iwr|invoke-webrequest)\b[^\n|]{0,200}\|\s*(sudo\s+)?(ba|z|da)?sh\b|\bbash\s+-c\b|\bpython3?\s+-c\b`),
	r(KindTool, "destructive", High, `(?i)\brm\s+-[a-z]*r[a-z]*f|\bgit\s+push\s+(-f|--force)|\bmkfs\b|\bdd\s+if=|\bchmod\s+-R\s+777|\bsystemctl\s+(stop|disable)\b`),
	r(KindTool, "bp-control", High, `(?i)\bbp\s+(msg|open|close|announce|wa|remote|shell|p2p|config|rename|reparent|account|update)\b|--force-busy|--dangerously|--no-sandbox`),
	r(KindTool, "slash-command", Warn, `(?m)^[ \t]*/[a-z][a-z0-9_-]{1,30}\b`),
	r(KindTool, "tool-use", Warn, `(?i)\b(use|call|invoke)\s+(the\s+)?(bash|shell|read|write|edit|webfetch|fetch|browser|mcp)\s+tool\b`),

	r(KindCredential, "ask-secret", High, `(?i)\b(send|give|share|paste|post|show|print|reveal|tell|read|cat|dump|upload|forward|leak)\b[^.\n]{0,40}\b(api[ _-]?keys?|tokens?|passwords?|passwd|credentials?|secrets?|private keys?|ssh keys?|session cookies?|cookies|\.env|env(ironment)? var(iable)?s?|identity\.key|seed phrase)\b`),
	r(KindCredential, "secret-path", High, `(?i)(~|\$HOME|/root|/home/[^/\s]+)/\.(ssh|aws|gnupg|docker|kube|config/gh|netrc|npmrc|pypirc|git-credentials)\b|\.credentials\.json|\bauth\.json\b|\bidentity\.key\b|\bpeers\.json\b|/etc/shadow|\bid_(rsa|ed25519|ecdsa)\b|(^|[/\s])\.env\b`),
	r(KindCredential, "secret-env", High, `\b(ANTHROPIC|OPENAI|OPENROUTER|GITHUB|GH|AWS|GOOGLE|STRIPE|BREVO|CLOUDFLARE|SLACK|HF)_[A-Z_]*(KEY|TOKEN|SECRET)\b|\bprintenv\b|\benv\s*\|`),

	r(KindExfil, "exfil-host", High, `(?i)\bhttps?://[^\s/]*(webhook\.site|requestbin|pipedream|ngrok|interact\.sh|oast\.|burpcollaborator|canarytokens|beeceptor|hookbin|trycloudflare\.com|serveo|localtunnel|loca\.lt|pastebin|transfer\.sh|0x0\.st|discord(app)?\.com/api/webhooks)`),
	r(KindExfil, "markdown-image", Warn, `!\[[^\]]*\]\(\s*https?://[^)\s]*\?[^)\s]*\)`),
	r(KindExfil, "ip-url", Warn, `(?i)\bhttps?://(\d{1,3}\.){3}\d{1,3}(:\d+)?\b|\bhttps?://\[[0-9a-f:]+\]`),
	r(KindExfil, "data-in-query", Warn, `(?i)\bhttps?://[^\s?]+\?[^\s]*(=\{|=\$\(|=\$\{|data=|secret=|key=|token=|q=\$)`),
	r(KindExfil, "send-to-url", Warn, `(?i)\b(send|post|upload|forward|exfiltrate|curl -d|curl --data)\b[^.\n]{0,60}\bhttps?://`),
}

var (
	base64Run = regexp.MustCompile(`[A-Za-z0-9+/_-]{60,}={0,2}`)
	hexRun    = regexp.MustCompile(`\b(?:[0-9a-fA-F]{2}){40,}\b`)
	decodeCmd = regexp.MustCompile(`(?i)\b(base64\s+(-d|--decode)|atob\(|b64decode|fromCharCode|xxd\s+-r|unhexlify)`)
)

// Scan flags steering patterns in text. It never modifies or drops the text.
func Scan(text string) []Finding {
	return scan(text, 0)
}

func scan(text string, depth int) []Finding {
	var out []Finding
	for _, rl := range rules {
		for _, loc := range rl.re.FindAllStringIndex(text, 4) {
			out = append(out, Finding{Kind: rl.kind, Rule: rl.name, Severity: rl.severity, Offset: loc[0], Excerpt: excerpt(text, loc[0], loc[1])})
		}
	}
	out = append(out, hidden(text)...)
	if loc := decodeCmd.FindStringIndex(text); loc != nil {
		out = append(out, Finding{Kind: KindEncoded, Rule: "decode-command", Severity: Warn, Offset: loc[0], Excerpt: excerpt(text, loc[0], loc[1])})
	}
	for _, loc := range base64Run.FindAllStringIndex(text, 8) {
		run := text[loc[0]:loc[1]]
		dec, ok := decodeBase64(run)
		if !ok {
			continue
		}
		out = append(out, Finding{Kind: KindEncoded, Rule: "base64", Severity: Warn, Offset: loc[0], Excerpt: excerpt(text, loc[0], loc[1])})
		out = append(out, nested(dec, loc[0], depth)...)
	}
	for _, loc := range hexRun.FindAllStringIndex(text, 8) {
		dec, err := hex.DecodeString(text[loc[0]:loc[1]])
		if err != nil || !mostlyText(dec) {
			continue
		}
		out = append(out, Finding{Kind: KindEncoded, Rule: "hex", Severity: Warn, Offset: loc[0], Excerpt: excerpt(text, loc[0], loc[1])})
		out = append(out, nested(string(dec), loc[0], depth)...)
	}
	return out
}

// nested rescans one decoded layer; anything found inside an encoded payload
// is at least Warn and High stays High.
func nested(dec string, offset, depth int) []Finding {
	if depth >= 1 {
		return nil
	}
	var out []Finding
	for _, f := range scan(dec, depth+1) {
		if f.Kind == KindEncoded {
			continue
		}
		f.Offset = offset
		f.Decoded = true
		out = append(out, f)
	}
	return out
}

func decodeBase64(s string) (string, bool) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && mostlyText(b) {
			return string(b), true
		}
	}
	return "", false
}

// mostlyText rejects binary blobs (keys, images, hashes) so only decodable
// prose or commands become findings.
func mostlyText(b []byte) bool {
	if len(b) < 16 || !utf8.Valid(b) {
		return false
	}
	printable := 0
	n := 0
	for _, r := range string(b) {
		n++
		if unicode.IsPrint(r) || r == '\n' || r == '\t' {
			printable++
		}
	}
	return printable*100/n >= 90
}

// hidden reports characters a reader cannot see but a model reads.
func hidden(text string) []Finding {
	var out []Finding
	counts := map[string]int{}
	first := map[string]int{}
	for i, r := range text {
		var name string
		switch {
		case r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\u2060' || r == '\ufeff' || r == '\u180e' || r == '\u00ad':
			name = "zero-width"
		case r >= 0xE0000 && r <= 0xE007F:
			name = "tag-characters"
		case isBidi(r):
			name = "bidi-control"
		case (r >= 0xFE00 && r <= 0xFE0F) || (r >= 0xE0100 && r <= 0xE01EF):
			name = "variation-selector"
		case (r >= 0xE000 && r <= 0xF8FF) || r >= 0xF0000:
			name = "private-use"
		case unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r':
			name = "control"
		default:
			continue
		}
		if counts[name] == 0 {
			first[name] = i
		}
		counts[name]++
	}
	for name, n := range counts {
		sev := Warn
		// Tag characters encode invisible ASCII; a handful is never accidental.
		if name == "tag-characters" || name == "bidi-control" || name == "control" || n >= 8 {
			sev = High
		}
		// A single variation selector follows emoji in ordinary text.
		if name == "variation-selector" && n < 4 {
			continue
		}
		out = append(out, Finding{Kind: KindHidden, Rule: name, Severity: sev, Offset: first[name], Excerpt: fmt.Sprintf("%d x %s", n, name)})
	}
	if tags := decodeTags(text); tags != "" {
		for _, f := range scan(tags, 1) {
			f.Decoded = true
			out = append(out, f)
		}
	}
	return out
}

// decodeTags reads the ASCII hidden in Unicode tag characters (U+E0020..7E).
func decodeTags(text string) string {
	var b strings.Builder
	for _, r := range text {
		if r >= 0xE0020 && r <= 0xE007E {
			b.WriteRune(r - 0xE0000)
		}
	}
	return b.String()
}

// excerpt returns at most 80 visible characters around a match, with control
// and invisible characters escaped and secrets redacted.
func excerpt(text string, start, end int) string {
	if end-start > 80 {
		end = start + 80
		for end > start && !utf8.RuneStart(text[end]) {
			end--
		}
	}
	s, _ := Redact(text[start:end], RedactPolicy{})
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || isBidi(r) || unicode.Is(unicode.Cf, r) || (r >= 0xE0000 && r <= 0xE007F) {
			fmt.Fprintf(&b, "\\u{%04X}", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// MaxSeverity returns the highest severity in fs, or "" when fs is empty.
func MaxSeverity(fs []Finding) Severity {
	var max Severity
	for _, f := range fs {
		switch {
		case f.Severity == High:
			return High
		case f.Severity == Warn:
			max = Warn
		case max == "":
			max = Info
		}
	}
	return max
}
