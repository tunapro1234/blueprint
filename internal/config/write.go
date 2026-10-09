package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"go.yaml.in/yaml/v3"
)

// SetModules rewrites only the modules key of the config file at path. Every
// other key keeps its value and position: JSON keeps its top-level key order
// and unknown keys, YAML keeps comments. The file is replaced atomically with
// its previous mode. Callers serialize writers with their own lock.
func SetModules(path string, modules map[string]bool) error {
	if path == "" {
		return fmt.Errorf("no config file to update")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var updated []byte
	if filepath.Ext(path) == ".json" {
		updated, err = setJSONModules(data, modules)
	} else {
		updated, err = setYAMLModules(data, modules)
	}
	if err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}
	if bytes.Equal(updated, data) {
		return nil
	}
	return writeAtomic(path, updated, info.Mode().Perm())
}

func sortedModuleNames(modules map[string]bool) []string {
	names := make([]string, 0, len(modules))
	for name := range modules {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func encodeModulesJSON(modules map[string]bool) []byte {
	var encoded bytes.Buffer
	encoded.WriteByte('{')
	for index, name := range sortedModuleNames(modules) {
		if index > 0 {
			encoded.WriteString(", ")
		}
		key, _ := json.Marshal(name)
		encoded.Write(key)
		fmt.Fprintf(&encoded, ": %t", modules[name])
	}
	encoded.WriteByte('}')
	return encoded.Bytes()
}

// setJSONModules edits the text in place: an existing top-level modules value
// is replaced, otherwise one member is inserted before the closing brace.
func setJSONModules(data []byte, modules map[string]bool) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("top level is not an object")
	}
	valueStart, valueEnd := -1, -1
	members := 0
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		afterKey := int(decoder.InputOffset())
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		members++
		if key == "modules" {
			valueEnd = int(decoder.InputOffset())
			colon := bytes.IndexByte(data[afterKey:], ':')
			if colon < 0 {
				return nil, errors.New("malformed modules member")
			}
			valueStart = afterKey + colon + 1
			for valueStart < valueEnd && isJSONSpace(data[valueStart]) {
				valueStart++
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	closing := int(decoder.InputOffset()) - 1
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after the top-level object")
	}
	encoded := encodeModulesJSON(modules)
	var out bytes.Buffer
	if valueStart >= 0 {
		out.Write(data[:valueStart])
		out.Write(encoded)
		out.Write(data[valueEnd:])
		return out.Bytes(), nil
	}
	// Insert after the last member, keeping whatever follows it (newline and
	// indentation of the closing brace).
	last := closing
	for last > 0 && isJSONSpace(data[last-1]) {
		last--
	}
	indent := detectJSONIndent(data)
	out.Write(data[:last])
	if members > 0 {
		out.WriteByte(',')
	}
	if indent == "" {
		out.WriteString(`"modules": `)
	} else {
		out.WriteString("\n" + indent + `"modules": `)
	}
	out.Write(encoded)
	if indent != "" && !bytes.ContainsRune(data[last:closing], '\n') {
		out.WriteByte('\n')
	}
	out.Write(data[last:])
	return out.Bytes(), nil
}

func isJSONSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

// detectJSONIndent returns the indentation of the first top-level member, or
// "" for a single-line object.
func detectJSONIndent(data []byte) string {
	open := bytes.IndexByte(data, '{')
	if open < 0 {
		return ""
	}
	rest := data[open+1:]
	newline := bytes.IndexByte(rest, '\n')
	if newline < 0 {
		return ""
	}
	line := rest[newline+1:]
	width := 0
	for width < len(line) && (line[width] == ' ' || line[width] == '\t') {
		width++
	}
	if width == 0 {
		return "  "
	}
	return string(line[:width])
}

// setYAMLModules replaces a one-line top-level modules entry or appends one,
// so the rest of the file (comments, blank lines, layout) stays byte for byte.
// A multi-line modules block is rewritten through yaml.Node.
func setYAMLModules(data []byte, modules map[string]bool) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	if document.Kind != 0 && (document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode) {
		return nil, errors.New("top level is not a mapping")
	}
	line := []byte("modules: " + string(encodeModulesYAML(modules)))
	var keyNode, valueNode *yaml.Node
	if document.Kind != 0 {
		root := document.Content[0]
		for index := 0; index+1 < len(root.Content); index += 2 {
			if root.Content[index].Value == "modules" {
				keyNode, valueNode = root.Content[index], root.Content[index+1]
			}
		}
	}
	if keyNode == nil {
		var out bytes.Buffer
		out.Write(data)
		if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
			out.WriteByte('\n')
		}
		out.Write(line)
		out.WriteByte('\n')
		return out.Bytes(), nil
	}
	if keyNode.Column == 1 && (valueNode.Line == keyNode.Line) && (valueNode.Kind == yaml.ScalarNode || valueNode.Style&yaml.FlowStyle != 0) {
		lines := bytes.SplitAfter(data, []byte("\n"))
		index := keyNode.Line - 1
		if index < len(lines) {
			old := lines[index]
			ending := ""
			if bytes.HasSuffix(old, []byte("\n")) {
				ending = "\n"
			}
			comment := ""
			if valueNode.LineComment != "" {
				comment = " " + valueNode.LineComment
			}
			lines[index] = []byte(string(line) + comment + ending)
			return bytes.Join(lines, nil), nil
		}
	}
	return setYAMLModulesNode(&document, modules)
}

func encodeModulesYAML(modules map[string]bool) []byte {
	var encoded bytes.Buffer
	encoded.WriteByte('{')
	for index, name := range sortedModuleNames(modules) {
		if index > 0 {
			encoded.WriteString(", ")
		}
		fmt.Fprintf(&encoded, "%s: %t", name, modules[name])
	}
	encoded.WriteByte('}')
	return encoded.Bytes()
}

func setYAMLModulesNode(document *yaml.Node, modules map[string]bool) ([]byte, error) {
	value := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
	for _, name := range sortedModuleNames(modules) {
		value.Content = append(value.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(modules[name])},
		)
	}
	root := document.Content[0]
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == "modules" {
			root.Content[index+1] = value
		}
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
