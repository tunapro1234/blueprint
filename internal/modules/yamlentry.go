package modules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	"blueprint/internal/safefile"
)

// KindYAMLEntry is one element bp added to a sequence in a user's YAML file (a
// harness hook config, for example). Option is a dotted path to the sequence
// from the document root ("hooks.pre_llm_call"), Value is the exact element
// re-encoded as JSON (YAML and JSON agree on the maps, sequences, strings,
// numbers and booleans a hook entry is built from, so jsonEqual already does
// the right comparison). Created, when set, is the dotted path of the first
// mapping key bp had to create on the way ("/" when bp created the file).
// Undo removes only an element deep-equal to Value; the rest of the document,
// including the user's own hooks and comments outside the touched nodes,
// keeps its content.
const KindYAMLEntry = "yaml-entry"

// AddYAMLEntry appends entry to the sequence at the dotted path in the YAML
// file at path, creating the file and any missing mapping keys on the way.
// When an element deep-equal to entry is already there — the user's own, or
// an earlier, already recorded add — nothing is written and the returned
// change is nil: undo must never remove what bp did not add.
func AddYAMLEntry(path, dotted string, entry map[string]any) (*Change, error) {
	keys := strings.Split(dotted, ".")
	if dotted == "" || len(keys) == 0 {
		return nil, fmt.Errorf("yaml-entry needs a dotted path to a sequence, got %q", dotted)
	}
	value, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	change := &Change{Kind: KindYAMLEntry, Path: path, Option: dotted, Value: string(value)}
	data, snap, err := safefile.Read(path)
	if err != nil {
		return nil, err
	}
	var document yaml.Node
	if snap.Exists && len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	if document.Kind == 0 {
		document = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: top level is not a mapping", path)
	}
	node := document.Content[0]
	created := ""
	for depth, key := range keys {
		found := findMapKey(node, key)
		last := depth == len(keys)-1
		if found == nil {
			var child *yaml.Node
			if last {
				child = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			} else {
				child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
			if created == "" {
				created = strings.Join(keys[:depth+1], ".")
			}
			found = child
		}
		if last {
			if found.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("%s: %s is not a YAML sequence", path, dotted)
			}
			for _, element := range found.Content {
				var existing map[string]any
				existingJSON, err := json.Marshal(decodeYAMLAny(element, &existing))
				if err == nil && jsonEqual(existingJSON, value) {
					return nil, nil
				}
			}
			entryNode := &yaml.Node{}
			if err := entryNode.Encode(entry); err != nil {
				return nil, err
			}
			found.Content = append(found.Content, entryNode)
			break
		}
		if found.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: %s is not a YAML mapping", path, strings.Join(keys[:depth+1], "."))
		}
		node = found
	}
	if !snap.Exists {
		change.Created = "/"
	} else if created != "" {
		change.Created = created
	}
	out, err := encodeYAMLDocument(&document)
	if err != nil {
		return nil, err
	}
	if err := safefile.Replace(snap, out, 0600); err != nil {
		return nil, err
	}
	return change, nil
}

func undoYAMLEntry(change Change) (string, error) {
	data, snap, err := safefile.Read(change.Path)
	if err != nil {
		return "", err
	}
	if !snap.Exists || len(bytes.TrimSpace(data)) == 0 {
		return "", nil
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return "", errModified
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return "", errModified
	}
	keys := strings.Split(change.Option, ".")
	node := document.Content[0]
	var sequence *yaml.Node
	for depth, key := range keys {
		found := findMapKey(node, key)
		if found == nil {
			return "", errModified
		}
		if depth == len(keys)-1 {
			if found.Kind != yaml.SequenceNode {
				return "", errModified
			}
			sequence = found
			break
		}
		if found.Kind != yaml.MappingNode {
			return "", errModified
		}
		node = found
	}
	match := -1
	for index, element := range sequence.Content {
		var existing map[string]any
		existingJSON, err := json.Marshal(decodeYAMLAny(element, &existing))
		if err == nil && jsonEqual(existingJSON, []byte(change.Value)) {
			match = index
			break
		}
	}
	if match < 0 {
		return "", errModified
	}
	sequence.Content = append(sequence.Content[:match], sequence.Content[match+1:]...)
	if change.Created == "/" {
		var remaining map[string]any
		// emptySkeleton (jsonentry.go) also accepts the empty mapping and
		// sequence nodes left behind on the way to the entry we just removed
		// (hooks: {pre_llm_call: []}), not just a literally empty document.
		if document.Content[0].Decode(&remaining) == nil && emptySkeleton(remaining) {
			if err := safefile.Remove(snap); err != nil {
				return "", err
			}
			return "removed " + change.Path, nil
		}
	}
	// A mapping key bp created on the way (change.Created, not "/") is left in
	// place even if it is now empty: a lone "pre_llm_call: []" is harmless, and
	// removing it would mean re-walking the tree for a container some other
	// change may since have populated.
	out, err := encodeYAMLDocument(&document)
	if err != nil {
		return "", err
	}
	if err := safefile.Replace(snap, out, 0600); err != nil {
		return "", err
	}
	return fmt.Sprintf("removed bp's entry from %s %s", change.Path, change.Option), nil
}

// decodeYAMLAny decodes node into *out and returns *out, so a caller can
// chain it into json.Marshal; a decode error leaves *out at its zero value
// (nil map), which never equals a real entry.
func decodeYAMLAny(node *yaml.Node, out *map[string]any) map[string]any {
	_ = node.Decode(out)
	return *out
}

func findMapKey(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func encodeYAMLDocument(document *yaml.Node) ([]byte, error) {
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
