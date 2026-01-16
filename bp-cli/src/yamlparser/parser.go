package yamlparser

import (
	"os"
	"strings"
)

// Parse parses blueprint content.
func Parse(content string) ParseResult {
	p := &parser{
		warnings: []string{},
		seenKeys: make(map[string]map[string]bool),
	}
	return p.parse(content)
}

// ParseFile reads a file and parses it.
func ParseFile(path string) ParseResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return ParseResult{
			Error: &ParseError{Message: err.Error()},
		}
	}
	return Parse(string(data))
}

type parser struct {
	lines    []parseLine
	idx      int
	warnings []string
	seenKeys map[string]map[string]bool
}

type parseLine struct {
	indent  int
	text    string
	lineNo  int
	rawText string
}

func (p *parser) parse(content string) ParseResult {
	if err := p.preprocessLines(content); err != nil {
		return ParseResult{Error: err}
	}

	for p.idx < len(p.lines) && strings.TrimSpace(p.lines[p.idx].text) == "" {
		p.idx++
	}

	if p.idx >= len(p.lines) {
		return ParseResult{Data: map[string]interface{}{}, Warnings: p.warnings}
	}

	val, err := p.parseBlock(p.lines[p.idx].indent, "")
	if err != nil {
		return ParseResult{Error: err, Warnings: p.warnings}
	}

	m, ok := val.(map[string]interface{})
	if !ok {
		return ParseResult{
			Error:    &ParseError{Line: 1, Message: "yaml root is not a map"},
			Warnings: p.warnings,
		}
	}

	return ParseResult{Data: m, Warnings: p.warnings}
}
