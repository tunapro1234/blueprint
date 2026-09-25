package cache

import (
	"bytes"
	"strings"
)

// HasCompactRecordTypePrefix cheaply recognizes a common compact JSONL prefix.
// It is only a positive hint; callers still decode the row as usual.
func HasCompactRecordTypePrefix(line []byte, types ...string) bool {
	for _, typeName := range types {
		prefix := []byte(`{"type":"` + typeName + `"`)
		if bytes.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// DefinitelyOtherCompactRecordType skips a compact row only when it starts
// with an unescaped top-level type, has no possible duplicate type key, and
// names a type outside wanted. Escaped or unusual JSON takes the full decoder.
func DefinitelyOtherCompactRecordType(line []byte, wanted ...string) bool {
	const prefix = `{"type":"`
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return false
	}
	valueStart := len(prefix)
	valueEnd := bytes.IndexByte(line[valueStart:], '"')
	if valueEnd < 0 {
		return false
	}
	valueEnd += valueStart
	if bytes.IndexByte(line[valueStart:valueEnd], '\\') >= 0 {
		return false
	}
	typeName := string(line[valueStart:valueEnd])
	for _, candidate := range wanted {
		if strings.EqualFold(typeName, candidate) {
			return false
		}
	}
	for i := valueEnd + 1; i < len(line); i++ {
		if line[i] == '\\' {
			return false
		}
		if line[i] == '"' && i+5 < len(line) && line[i+5] == '"' && equalFoldType(line[i+1:i+5]) {
			return false
		}
	}
	return true
}

func equalFoldType(value []byte) bool {
	return len(value) == 4 && (value[0] == 't' || value[0] == 'T') && (value[1] == 'y' || value[1] == 'Y') && (value[2] == 'p' || value[2] == 'P') && (value[3] == 'e' || value[3] == 'E')
}

// MayHaveRecordType returns false only when it can safely prove that a valid
// top-level JSON object has a different type. Ambiguous JSON is left to the
// regular decoder, so this prefilter cannot change malformed-row behavior.
func MayHaveRecordType(line []byte, wanted ...string) bool {
	typeName, ok := topLevelRecordType(line)
	if !ok {
		return true
	}
	for _, candidate := range wanted {
		if strings.EqualFold(typeName, candidate) {
			return true
		}
	}
	return false
}

func topLevelRecordType(data []byte) (string, bool) {
	i := skipJSONSpace(data, 0)
	if i >= len(data) || data[i] != '{' {
		return "", false
	}
	i++
	typeName, found := "", false
	for {
		i = skipJSONSpace(data, i)
		if i >= len(data) {
			return "", false
		}
		if data[i] == '}' {
			i = skipJSONSpace(data, i+1)
			if i != len(data) {
				return "", false
			}
			return typeName, true
		}
		keyStart := i
		keyEnd, escaped, ok := scanJSONString(data, i)
		if !ok || escaped {
			return "", false
		}
		key := data[keyStart+1 : keyEnd-1]
		i = skipJSONSpace(data, keyEnd)
		if i >= len(data) || data[i] != ':' {
			return "", false
		}
		i = skipJSONSpace(data, i+1)
		if strings.EqualFold(string(key), "type") {
			if found {
				return "", false
			}
			valueStart := i
			valueEnd, valueEscaped, ok := scanJSONString(data, i)
			if !ok || valueEscaped {
				return "", false
			}
			typeName = string(data[valueStart+1 : valueEnd-1])
			found = true
			i = valueEnd
		} else {
			var ok bool
			i, ok = skipJSONValue(data, i)
			if !ok {
				return "", false
			}
		}
		i = skipJSONSpace(data, i)
		if i >= len(data) {
			return "", false
		}
		switch data[i] {
		case ',':
			i++
		case '}':
			i = skipJSONSpace(data, i+1)
			if i != len(data) {
				return "", false
			}
			return typeName, true
		default:
			return "", false
		}
	}
}

func skipJSONSpace(data []byte, i int) int {
	for i < len(data) {
		switch data[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

func scanJSONString(data []byte, start int) (int, bool, bool) {
	if start >= len(data) || data[start] != '"' {
		return start, false, false
	}
	escaped := false
	for i := start + 1; i < len(data); i++ {
		switch data[i] {
		case '"':
			return i + 1, escaped, true
		case '\\':
			escaped = true
			i++
			if i >= len(data) {
				return start, false, false
			}
		case '\n', '\r':
			return start, false, false
		}
	}
	return start, false, false
}

func skipJSONValue(data []byte, start int) (int, bool) {
	if start >= len(data) {
		return start, false
	}
	switch data[start] {
	case '"':
		end, _, ok := scanJSONString(data, start)
		return end, ok
	case '{', '[':
		stack := []byte{'}'}
		if data[start] == '[' {
			stack[0] = ']'
		}
		for i := start + 1; i < len(data); i++ {
			switch data[i] {
			case '"':
				end, _, ok := scanJSONString(data, i)
				if !ok {
					return start, false
				}
				i = end - 1
			case '{':
				stack = append(stack, '}')
			case '[':
				stack = append(stack, ']')
			case '}', ']':
				if len(stack) == 0 || stack[len(stack)-1] != data[i] {
					return start, false
				}
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					return i + 1, true
				}
			}
		}
		return start, false
	default:
		end := start
		for end < len(data) {
			switch data[end] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				if end == start {
					return start, false
				}
				return end, true
			default:
				end++
			}
		}
		return end, end > start
	}
}
