package modules

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// KindJSONEntry is one element bp added to an array in a user's JSON file
// (a harness settings file, for example). Option is the JSON pointer of the
// array, Value the exact element. Created, when set, is the pointer of the
// first container bp had to create on the way ("/" when bp created the file).
// Undo removes only an element deep-equal to Value; every other byte of the
// file stays as it is.
const KindJSONEntry = "json-entry"

// AddJSONEntry appends entry to the array at pointer in the JSON file at path,
// creating the file and missing objects on the way, and returns the change to
// record. The file is edited as text, so the user's formatting, key order and
// other entries are kept byte for byte. An element already deep-equal to entry
// is not added twice.
func AddJSONEntry(path, pointer string, entry any) (Change, error) {
	value, err := json.Marshal(entry)
	if err != nil {
		return Change{}, err
	}
	keys, err := splitPointer(pointer)
	if err != nil || len(keys) == 0 {
		return Change{}, fmt.Errorf("json-entry needs the pointer of an array inside an object, got %q", pointer)
	}
	change := Change{Kind: KindJSONEntry, Path: path, Option: pointer, Value: string(value)}
	real := resolvePath(path)
	data, err := os.ReadFile(real)
	if errors.Is(err, os.ErrNotExist) {
		var doc any = []any{json.RawMessage(value)}
		for index := len(keys) - 1; index >= 0; index-- {
			doc = map[string]any{keys[index]: doc}
		}
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return Change{}, err
		}
		change.Created = "/"
		return change, writeFileAtomic(real, append(out, '\n'), 0600)
	}
	if err != nil {
		return Change{}, err
	}
	mode := os.FileMode(0600)
	if info, err := os.Stat(real); err == nil {
		mode = info.Mode().Perm()
	}
	start := skipSpace(data, 0)
	if start >= len(data) || data[start] != '{' {
		return Change{}, fmt.Errorf("%s: not a JSON object", path)
	}
	at := start
	for depth, key := range keys {
		members, err := objectMembers(data, at)
		if err != nil {
			return Change{}, fmt.Errorf("%s: %w", path, err)
		}
		found := -1
		for index, member := range members {
			if member.key == key {
				found = index
				break
			}
		}
		if found < 0 {
			// Create the rest of the chain inside this object.
			var rest any = []any{json.RawMessage(value)}
			for index := len(keys) - 1; index > depth; index-- {
				rest = map[string]any{keys[index]: rest}
			}
			out, err := insertMember(data, at, members, key, rest)
			if err != nil {
				return Change{}, err
			}
			change.Created = joinPointer(keys[:depth+1])
			return change, writeFileAtomic(real, out, mode)
		}
		at = members[found].valueStart
		want := byte('{')
		if depth == len(keys)-1 {
			want = '['
		}
		if data[at] != want {
			return Change{}, fmt.Errorf("%s: %s is not a JSON %s", path, joinPointer(keys[:depth+1]), map[byte]string{'{': "object", '[': "array"}[want])
		}
	}
	elements, err := arrayElements(data, at)
	if err != nil {
		return Change{}, fmt.Errorf("%s: %w", path, err)
	}
	for _, element := range elements {
		if jsonEqual(data[element.start:element.end], value) {
			return change, nil
		}
	}
	var out []byte
	if len(elements) == 0 {
		out = splice(data, at+1, at+1, value)
	} else {
		last := elements[len(elements)-1]
		indent := leadingSpace(data, elements, len(elements)-1, at)
		out = splice(data, last.end, last.end, append(append([]byte{','}, indent...), value...))
	}
	return change, writeFileAtomic(real, out, mode)
}

func undoJSONEntry(change Change) (string, error) {
	real := resolvePath(change.Path)
	data, err := os.ReadFile(real)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	keys, err := splitPointer(change.Option)
	if err != nil {
		return "", err
	}
	at, err := locate(data, keys)
	if err != nil || data[at] != '[' {
		return "", errModified
	}
	elements, err := arrayElements(data, at)
	if err != nil {
		return "", err
	}
	match := -1
	for index, element := range elements {
		if jsonEqual(data[element.start:element.end], []byte(change.Value)) {
			match = index
			break
		}
	}
	if match < 0 {
		return "", errModified
	}
	data = removeSpan(data, elements, match, at)
	if change.Created != "" {
		if change.Created == "/" {
			var doc any
			if json.Unmarshal(data, &doc) == nil && emptySkeleton(doc) {
				if err := os.Remove(real); err != nil {
					return "", err
				}
				return "removed " + change.Path, nil
			}
		} else if created, err := splitPointer(change.Created); err == nil && len(created) > 0 {
			parent, err := locate(data, created[:len(created)-1])
			if err == nil && data[parent] == '{' {
				members, err := objectMembers(data, parent)
				if err == nil {
					for index, member := range members {
						if member.key != created[len(created)-1] {
							continue
						}
						var value any
						if json.Unmarshal(data[member.valueStart:member.end], &value) == nil && emptySkeleton(value) {
							data = removeMember(data, members, index, parent)
						}
						break
					}
				}
			}
		}
	}
	if err := writeFileAtomic(real, data, info.Mode().Perm()); err != nil {
		return "", err
	}
	return fmt.Sprintf("removed bp's entry from %s %s", change.Path, change.Option), nil
}

// resolvePath follows a symlinked settings file so bp edits the target and
// the link stays a link.
func resolvePath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

func splitPointer(pointer string) ([]string, error) {
	if pointer == "" || pointer == "/" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("JSON pointer %q must start with /", pointer)
	}
	parts := strings.Split(pointer[1:], "/")
	for index, part := range parts {
		parts[index] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

func joinPointer(keys []string) string {
	var out strings.Builder
	for _, key := range keys {
		out.WriteString("/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1"))
	}
	return out.String()
}

type span struct {
	keyStart, valueStart, start, end int
	key                              string
}

func skipSpace(data []byte, at int) int {
	for at < len(data) && (data[at] == ' ' || data[at] == '\t' || data[at] == '\n' || data[at] == '\r') {
		at++
	}
	return at
}

func valueEnd(data []byte, at int) (int, error) {
	decoder := json.NewDecoder(bytes.NewReader(data[at:]))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return 0, err
	}
	return at + int(decoder.InputOffset()), nil
}

// objectMembers lists the members of the object starting at data[at] == '{'.
func objectMembers(data []byte, at int) ([]span, error) {
	var members []span
	index := skipSpace(data, at+1)
	if index < len(data) && data[index] == '}' {
		return nil, nil
	}
	for index < len(data) {
		keyEnd, err := valueEnd(data, index)
		if err != nil {
			return nil, err
		}
		var key string
		if err := json.Unmarshal(data[index:keyEnd], &key); err != nil {
			return nil, err
		}
		colon := skipSpace(data, keyEnd)
		if colon >= len(data) || data[colon] != ':' {
			return nil, errors.New("malformed object")
		}
		valueStart := skipSpace(data, colon+1)
		end, err := valueEnd(data, valueStart)
		if err != nil {
			return nil, err
		}
		members = append(members, span{key: key, keyStart: index, start: index, valueStart: valueStart, end: end})
		next := skipSpace(data, end)
		if next < len(data) && data[next] == '}' {
			return members, nil
		}
		if next >= len(data) || data[next] != ',' {
			return nil, errors.New("malformed object")
		}
		index = skipSpace(data, next+1)
	}
	return nil, errors.New("unterminated object")
}

// arrayElements lists the elements of the array starting at data[at] == '['.
func arrayElements(data []byte, at int) ([]span, error) {
	var elements []span
	index := skipSpace(data, at+1)
	if index < len(data) && data[index] == ']' {
		return nil, nil
	}
	for index < len(data) {
		end, err := valueEnd(data, index)
		if err != nil {
			return nil, err
		}
		elements = append(elements, span{start: index, valueStart: index, end: end})
		next := skipSpace(data, end)
		if next < len(data) && data[next] == ']' {
			return elements, nil
		}
		if next >= len(data) || data[next] != ',' {
			return nil, errors.New("malformed array")
		}
		index = skipSpace(data, next+1)
	}
	return nil, errors.New("unterminated array")
}

// locate returns the offset of the value at keys (object members only).
func locate(data []byte, keys []string) (int, error) {
	at := skipSpace(data, 0)
	for _, key := range keys {
		if at >= len(data) || data[at] != '{' {
			return 0, errModified
		}
		members, err := objectMembers(data, at)
		if err != nil {
			return 0, err
		}
		found := false
		for _, member := range members {
			if member.key == key {
				at, found = member.valueStart, true
				break
			}
		}
		if !found {
			return 0, errModified
		}
	}
	return at, nil
}

// leadingSpace is the whitespace before item index (after the separator or
// the opening bracket), reused so an added item looks like its neighbours.
func leadingSpace(data []byte, items []span, index, open int) []byte {
	from := open + 1
	if index > 0 {
		from = skipComma(data, items[index-1].end)
	}
	return append([]byte(nil), data[from:items[index].start]...)
}

func skipComma(data []byte, at int) int {
	at = skipSpace(data, at)
	if at < len(data) && data[at] == ',' {
		at++
	}
	return at
}

// removeSpan cuts item index out of its array or object, together with the
// separator that AddJSONEntry/insertMember put in front of it, so add then
// remove returns the original bytes.
func removeSpan(data []byte, items []span, index, open int) []byte {
	item := items[index]
	switch {
	case index > 0:
		return splice(data, items[index-1].end, item.end, nil)
	case len(items) > 1:
		return splice(data, item.start, items[1].start, nil)
	default:
		return splice(data, item.start, item.end, nil)
	}
}

func removeMember(data []byte, members []span, index, open int) []byte {
	return removeSpan(data, members, index, open)
}

func insertMember(data []byte, at int, members []span, key string, value any) ([]byte, error) {
	indent := []byte("\n  ")
	if len(members) > 0 {
		indent = leadingSpace(data, members, len(members)-1, at)
	}
	unit := "  "
	if trimmed := strings.TrimLeft(string(indent), "\r\n"); trimmed != "" {
		unit = trimmed
	}
	encodedValue, err := json.MarshalIndent(value, strings.TrimLeft(string(indent), "\r\n"), indentUnit(unit))
	if err != nil {
		return nil, err
	}
	encodedKey, _ := json.Marshal(key)
	member := append(append(encodedKey, ": "...), encodedValue...)
	if len(members) == 0 {
		return splice(data, at+1, at+1, member), nil
	}
	last := members[len(members)-1]
	return splice(data, last.end, last.end, append(append([]byte{','}, indent...), member...)), nil
}

// indentUnit guesses one indentation step from a member's full indentation.
func indentUnit(indent string) string {
	if strings.HasPrefix(indent, "\t") {
		return "\t"
	}
	if len(indent) >= 2 && len(indent)%2 == 0 {
		return "  "
	}
	return indent
}

func splice(data []byte, from, to int, insert []byte) []byte {
	out := make([]byte, 0, len(data)-(to-from)+len(insert))
	out = append(out, data[:from]...)
	out = append(out, insert...)
	return append(out, data[to:]...)
}

func jsonEqual(a, b []byte) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

// emptySkeleton is true for empty arrays and for objects that hold nothing
// but other empty skeletons: what is left of containers bp created once its
// entry is gone.
func emptySkeleton(value any) bool {
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case map[string]any:
		for _, child := range typed {
			if !emptySkeleton(child) {
				return false
			}
		}
		return true
	}
	return false
}
