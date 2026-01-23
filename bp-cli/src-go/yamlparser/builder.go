package yamlparser

import (
	"strings"
)

func (p *parser) parseBlock(indent int, path string) (interface{}, *ParseError) {
	for p.idx < len(p.lines) {
		line := p.lines[p.idx]
		if strings.TrimSpace(line.text) != "" {
			break
		}
		p.idx++
	}

	if p.idx >= len(p.lines) {
		return nil, nil
	}

	line := p.lines[p.idx]
	if strings.HasPrefix(strings.TrimSpace(line.text), "-") {
		return p.parseList(indent, path)
	}
	return p.parseMap(indent, path)
}

func (p *parser) parseMap(indent int, path string) (map[string]interface{}, *ParseError) {
	m := map[string]interface{}{}
	lastKey := ""
	if p.seenKeys[path] == nil {
		p.seenKeys[path] = make(map[string]bool)
	}

	for p.idx < len(p.lines) {
		line := p.lines[p.idx]
		if strings.TrimSpace(line.text) == "" {
			p.idx++
			continue
		}
		if line.indent < indent {
			break
		}
		if line.indent > indent {
			return nil, &ParseError{Line: line.lineNo, Column: line.indent + 1, Message: "unexpected indent", Context: line.rawText}
		}

		trimmed := strings.TrimSpace(line.text)
		colIdx := findFirstColon(trimmed)
		if colIdx == -1 {
			if lastKey != "" {
				if prev, ok := m[lastKey].(string); ok {
					m[lastKey] = prev + "\n" + strings.TrimSpace(trimmed)
					p.idx++
					continue
				}
			}
			return nil, &ParseError{Line: line.lineNo, Column: line.indent + 1, Message: "missing ':'", Context: line.rawText}
		}

		key, val, err := p.splitKeyValue(trimmed, line.lineNo, line.indent)
		if err != nil {
			return nil, err
		}

		if p.seenKeys[path][key] {
			p.warnings = append(p.warnings, "Duplicate key: "+key)
		}
		p.seenKeys[path][key] = true

		p.idx++

		if val == "" {
			nextIndent := p.peekNextIndent()
			if nextIndent > indent {
				childPath := path + "." + key
				child, err := p.parseBlock(nextIndent, childPath)
				if err != nil {
					return nil, err
				}
				m[key] = child
			} else {
				m[key] = nil
			}
			lastKey = key
			continue
		}

		if val == "|" || val == ">" {
			text, err := p.parseMultiline(indent, val)
			if err != nil {
				return nil, err
			}
			m[key] = text
			lastKey = key
			continue
		}

		col := line.indent + colIdx + 2
		parsed, err := parseScalar(val, line.lineNo, col)
		if err != nil {
			return nil, err
		}
		m[key] = parsed
		lastKey = key
	}

	return m, nil
}

func (p *parser) parseList(indent int, path string) ([]interface{}, *ParseError) {
	list := []interface{}{}
	for p.idx < len(p.lines) {
		line := p.lines[p.idx]
		if strings.TrimSpace(line.text) == "" {
			p.idx++
			continue
		}
		if line.indent < indent {
			break
		}
		if line.indent > indent {
			return nil, &ParseError{Line: line.lineNo, Column: line.indent + 1, Message: "unexpected indent", Context: line.rawText}
		}

		text := strings.TrimSpace(line.text)
		if !strings.HasPrefix(text, "-") {
			break
		}

		rest := strings.TrimSpace(strings.TrimPrefix(text, "-"))
		p.idx++

		if rest == "" {
			nextIndent := p.peekNextIndent()
			if nextIndent > indent {
				child, err := p.parseBlock(nextIndent, path)
				if err != nil {
					return nil, err
				}
				list = append(list, child)
			} else {
				list = append(list, nil)
			}
			continue
		}

		if rest == "|" || rest == ">" {
			text, err := p.parseMultiline(indent, rest)
			if err != nil {
				return nil, err
			}
			list = append(list, text)
			continue
		}

		if colIdx := findFirstColon(rest); colIdx != -1 {
			item, err := p.parseInlineMapItem(indent, rest)
			if err != nil {
				return nil, err
			}
			list = append(list, item)
			continue
		}

		parsed, err := parseScalar(rest, line.lineNo, line.indent+3)
		if err != nil {
			return nil, err
		}
		list = append(list, parsed)
	}
	return list, nil
}

func (p *parser) parseInlineMapItem(indent int, rest string) (map[string]interface{}, *ParseError) {
	lineNo := p.lines[p.idx-1].lineNo
	key, val, err := p.splitKeyValue(rest, lineNo, indent+2)
	if err != nil {
		return nil, err
	}

	item := map[string]interface{}{}
	if val == "" {
		nextIndent := p.peekNextIndent()
		if nextIndent > indent {
			child, err := p.parseBlock(nextIndent, "")
			if err != nil {
				return nil, err
			}
			item[key] = child
		} else {
			item[key] = nil
		}
		return item, nil
	}

	if val == "|" || val == ">" {
		text, err := p.parseMultiline(indent+2, val)
		if err != nil {
			return nil, err
		}
		item[key] = text
		return item, nil
	}

	parsed, err := parseScalar(val, lineNo, indent+4)
	if err != nil {
		return nil, err
	}
	item[key] = parsed

	nextIndent := p.peekNextIndent()
	if nextIndent > indent {
		childMap, err := p.parseMap(nextIndent, "")
		if err != nil {
			return nil, err
		}
		for k, v := range childMap {
			item[k] = v
		}
	}

	return item, nil
}

func (p *parser) splitKeyValue(line string, lineNo int, indent int) (string, string, *ParseError) {
	colIdx := findFirstColon(line)
	if colIdx == -1 {
		return "", "", &ParseError{Line: lineNo, Column: indent + 1, Message: "missing ':'"}
	}

	key := strings.TrimSpace(line[:colIdx])
	if key == "" {
		return "", "", &ParseError{Line: lineNo, Column: indent + 1, Message: "empty key"}
	}

	unquotedKey, err := unquoteValue(key, lineNo, indent+1)
	if err != nil {
		return "", "", err
	}
	key = unquotedKey

	val := strings.TrimSpace(line[colIdx+1:])
	_, _, err = unquoteValueIfQuoted(val, lineNo, indent+colIdx+2)
	if err != nil {
		return "", "", err
	}
	return key, val, nil
}

func (p *parser) peekNextIndent() int {
	idx := p.idx
	for idx < len(p.lines) {
		line := p.lines[idx]
		if strings.TrimSpace(line.text) != "" {
			return line.indent
		}
		idx++
	}
	return -1
}

func (p *parser) parseMultiline(parentIndent int, style string) (string, *ParseError) {
	start := p.idx
	baseIndent := -1
	var out []string

	for p.idx < len(p.lines) {
		line := p.lines[p.idx]
		if strings.TrimSpace(line.text) == "" {
			if baseIndent == -1 {
				p.idx++
				continue
			}
			out = append(out, "")
			p.idx++
			continue
		}
		if line.indent <= parentIndent {
			break
		}
		if baseIndent == -1 {
			baseIndent = line.indent
		}
		if line.indent < baseIndent {
			break
		}
		trimmed := strings.TrimRight(line.text, "\r")
		out = append(out, trimmed)
		p.idx++
	}

	if baseIndent == -1 {
		lineNo := 0
		if start-1 >= 0 && start-1 < len(p.lines) {
			lineNo = p.lines[start-1].lineNo
		}
		return "", &ParseError{Line: lineNo, Message: "expected multiline block"}
	}

	for i := range out {
		if len(out[i]) >= baseIndent {
			out[i] = out[i][baseIndent:]
		} else {
			out[i] = ""
		}
	}

	if style == "|" {
		return strings.Join(out, "\n") + "\n", nil
	}
	return foldLines(out) + "\n", nil
}

func foldLines(lines []string) string {
	var b strings.Builder
	prevBlank := false
	for i, line := range lines {
		if line == "" {
			b.WriteString("\n")
			prevBlank = true
			continue
		}
		if i > 0 && !prevBlank {
			b.WriteString(" ")
		}
		b.WriteString(line)
		prevBlank = false
	}
	return b.String()
}

func parseScalar(val string, lineNo int, col int) (interface{}, *ParseError) {
	if unquoted, wasQuoted, err := unquoteValueIfQuoted(val, lineNo, col); err != nil {
		return nil, err
	} else if wasQuoted {
		return unquoted, nil
	}

	lower := strings.ToLower(val)
	switch lower {
	case "true", "yes":
		return true, nil
	case "false", "no":
		return false, nil
	case "null", "~":
		return nil, nil
	}
	return val, nil
}

func unquoteValue(val string, lineNo int, col int) (string, *ParseError) {
	if val == "" {
		return val, nil
	}
	if val[0] == '"' || val[0] == '\'' {
		closeIdx := strings.IndexByte(val[1:], val[0])
		if closeIdx == -1 {
			return "", &ParseError{Line: lineNo, Column: col, Message: "unclosed quote"}
		}
		if closeIdx == len(val)-2 {
			return val[1 : len(val)-1], nil
		}
		return val, nil
	}
	return val, nil
}

func unquoteValueIfQuoted(val string, lineNo int, col int) (string, bool, *ParseError) {
	if val == "" {
		return val, false, nil
	}
	if val[0] == '"' || val[0] == '\'' {
		closeIdx := strings.IndexByte(val[1:], val[0])
		if closeIdx == -1 {
			return "", false, &ParseError{Line: lineNo, Column: col, Message: "unclosed quote"}
		}
		if closeIdx == len(val)-2 {
			return val[1 : len(val)-1], true, nil
		}
		return val, false, nil
	}
	return val, false, nil
}
