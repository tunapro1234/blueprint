package guard

import (
	"regexp"
	"sort"
	"strings"
)

// RedactPolicy configures outbound redaction for one peer. The zero value
// redacts every built-in secret format and leaves emails alone.
type RedactPolicy struct {
	// Emails also redacts email addresses.
	Emails bool `json:"emails,omitempty" yaml:"emails,omitempty"`
	// Patterns are extra regular expressions, each redacted as "custom".
	Patterns []string `json:"patterns,omitempty" yaml:"patterns,omitempty"`
	// Literals are exact secret values known to this machine (for example
	// read from credential files at start-up). Values shorter than 8 bytes
	// are ignored so common words are never redacted. Never serialised.
	Literals []string `json:"-" yaml:"-"`
	// Allow lists exact strings that must never be redacted (public keys,
	// Peer IDs the owner chose to share).
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
}

type secretPattern struct {
	name string
	re   *regexp.Regexp
	// group, when >0, redacts only that submatch (the value, not the key).
	group int
}

var secretPatterns = []secretPattern{
	{"private-key", regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----[\s\S]*?(-----END [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----|$)`), 0},
	{"anthropic-key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`), 0},
	{"openai-key", regexp.MustCompile(`\bsk-(proj-|svcacct-|admin-)?[A-Za-z0-9_-]{20,}`), 0},
	{"openrouter-key", regexp.MustCompile(`\bsk-or-v1-[a-f0-9]{32,}`), 0},
	{"github-token", regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{22,})`), 0},
	{"gitlab-token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`), 0},
	{"aws-access-key", regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`), 0},
	{"google-api-key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`), 0},
	{"google-oauth", regexp.MustCompile(`\bya29\.[0-9A-Za-z_-]{20,}`), 0},
	{"slack-token", regexp.MustCompile(`\bxox[abprse]-[A-Za-z0-9-]{10,}`), 0},
	{"stripe-key", regexp.MustCompile(`\b(sk|rk|pk)_(live|test)_[0-9a-zA-Z]{16,}`), 0},
	{"brevo-key", regexp.MustCompile(`\bxkeysib-[a-f0-9]{32,}-[A-Za-z0-9]{8,}`), 0},
	{"huggingface-token", regexp.MustCompile(`\bhf_[A-Za-z0-9]{30,}`), 0},
	{"npm-token", regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`), 0},
	{"jwt", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), 0},
	{"libp2p-private-key", regexp.MustCompile(`\bCAES[QI][A-Za-z0-9+/]{80,}={0,2}`), 0},
	{"url-credentials", regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s:/@]+:([^\s@/]{4,})@`), 1},
	{"bearer", regexp.MustCompile(`(?i)\b(?:bearer|token)\s+([A-Za-z0-9._~+/-]{20,}=*)`), 1},
	{"assignment", regexp.MustCompile(`(?i)\b[A-Z0-9_.-]*(?:api[_-]?key|secret|token|passw(?:or)?d|pwd|private[_-]?key|access[_-]?key|client[_-]?secret|auth)[A-Z0-9_.-]*["']?\s*[:=]\s*["']?([^\s"',;]{8,})`), 1},
}

var emailPattern = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)

type span struct {
	start, end int
	kind       string
}

// Redact replaces secrets in outbound text with "[redacted:<kind>]" and
// returns one finding per replacement. Excerpts never contain the secret.
func Redact(text string, p RedactPolicy) (string, []Finding) {
	var spans []span
	add := func(s, e int, kind string) {
		v := text[s:e]
		for _, a := range p.Allow {
			if a != "" && strings.Contains(a, v) {
				return
			}
		}
		spans = append(spans, span{s, e, kind})
	}
	for _, sp := range secretPatterns {
		for _, m := range sp.re.FindAllStringSubmatchIndex(text, -1) {
			s, e := m[0], m[1]
			if sp.group > 0 {
				s, e = m[2*sp.group], m[2*sp.group+1]
			}
			if s >= 0 && e > s {
				add(s, e, sp.name)
			}
		}
	}
	if p.Emails {
		for _, m := range emailPattern.FindAllStringIndex(text, -1) {
			add(m[0], m[1], "email")
		}
	}
	for _, expr := range p.Patterns {
		re, err := regexp.Compile(expr)
		if err != nil {
			continue
		}
		for _, m := range re.FindAllStringIndex(text, -1) {
			if m[1] > m[0] {
				add(m[0], m[1], "custom")
			}
		}
	}
	for _, lit := range p.Literals {
		if len(lit) < 8 {
			continue
		}
		for i := 0; ; {
			j := strings.Index(text[i:], lit)
			if j < 0 {
				break
			}
			add(i+j, i+j+len(lit), "known-secret")
			i += j + len(lit)
		}
	}
	if len(spans) == 0 {
		return text, nil
	}
	spans = merge(spans)
	var b strings.Builder
	var out []Finding
	prev := 0
	for _, sp := range spans {
		b.WriteString(text[prev:sp.start])
		b.WriteString("[redacted:" + sp.kind + "]")
		out = append(out, Finding{Kind: KindSecret, Rule: sp.kind, Severity: High, Offset: sp.start, Excerpt: "[redacted:" + sp.kind + "]"})
		prev = sp.end
	}
	b.WriteString(text[prev:])
	return b.String(), out
}

// merge sorts spans and joins overlaps, keeping the kind of the earliest,
// longest span.
func merge(spans []span) []span {
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})
	out := spans[:1]
	for _, sp := range spans[1:] {
		last := &out[len(out)-1]
		if sp.start < last.end {
			if sp.end > last.end {
				last.end = sp.end
			}
			continue
		}
		out = append(out, sp)
	}
	return out
}
