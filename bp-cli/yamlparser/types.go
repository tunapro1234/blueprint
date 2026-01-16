package yamlparser

import "fmt"

// ParseResult is the result of parsing a blueprint file.
type ParseResult struct {
	Data     map[string]interface{}
	Warnings []string
	Error    *ParseError
}

// ParseError contains details about a parse error.
type ParseError struct {
	Line    int
	Column  int
	Message string
	Context string
}

func (e *ParseError) Error() string {
	if e == nil {
		return ""
	}
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Message)
	}
	return e.Message
}
