package yamlparser

import (
	"strings"
)

func (p *parser) preprocessLines(content string) *ParseError {
	raw := strings.Split(content, "\n")
	p.lines = make([]parseLine, 0, len(raw))

	inBlock := false
	blockIndent := 0
	warnedTabs := false

	for i, line := range raw {
		lineNo := i + 1
		rawLine := strings.TrimRight(line, "\r")
		normalized, indent, hadTab := normalizeIndent(rawLine)
		if hadTab && !warnedTabs {
			p.warnings = append(p.warnings, "Tab characters")
			warnedTabs = true
		}
		rawLine = normalized

		if inBlock {
			if strings.TrimSpace(rawLine) == "" {
				p.lines = append(p.lines, parseLine{indent: indent, text: "", lineNo: lineNo, rawText: rawLine})
				continue
			}
			if indent <= blockIndent && strings.TrimSpace(rawLine) != "" {
				inBlock = false
			} else {
				p.lines = append(p.lines, parseLine{indent: indent, text: rawLine, lineNo: lineNo, rawText: rawLine})
				continue
			}
		}

		stripped := stripComment(rawLine)
		if strings.TrimSpace(stripped) == "" {
			p.lines = append(p.lines, parseLine{indent: indent, text: "", lineNo: lineNo, rawText: rawLine})
			continue
		}

		text := strings.TrimSpace(stripped)
		p.lines = append(p.lines, parseLine{indent: indent, text: stripped, lineNo: lineNo, rawText: rawLine})

		if val := valueAfterFirstColon(text); val == "|" || val == ">" {
			inBlock = true
			if strings.HasPrefix(text, "- ") {
				blockIndent = indent + 2
			} else {
				blockIndent = indent
			}
		}
	}

	return nil
}

func normalizeIndent(line string) (string, int, bool) {
	indent := 0
	i := 0
	hadTab := false
	for i < len(line) {
		if line[i] == ' ' {
			indent++
			i++
			continue
		}
		if line[i] == '\t' {
			indent += 2
			hadTab = true
			i++
			continue
		}
		break
	}
	if !hadTab {
		return line, indent, false
	}
	return strings.Repeat(" ", indent) + line[i:], indent, true
}

func stripComment(line string) string {
	inQuote := false
	quoteChar := rune(0)

	for i, r := range line {
		if !inQuote {
			if r == '"' || r == '\'' {
				inQuote = true
				quoteChar = r
			} else if r == '#' {
				if i == 0 {
					return ""
				}
				before := line[:i]
				if strings.HasSuffix(before, " ") || strings.HasSuffix(before, "\t") {
					return strings.TrimRight(before, " \t")
				}
			}
		} else if r == quoteChar {
			inQuote = false
		}
	}
	return line
}

func valueAfterFirstColon(text string) string {
	col := findFirstColon(text)
	if col == -1 {
		return ""
	}
	return strings.TrimSpace(text[col+1:])
}

func findFirstColon(text string) int {
	inQuote := false
	quoteChar := rune(0)
	for i, r := range text {
		if !inQuote {
			if r == '"' || r == '\'' {
				inQuote = true
				quoteChar = r
			} else if r == ':' {
				return i
			}
		} else if r == quoteChar {
			inQuote = false
		}
	}
	return -1
}
