package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"go.yaml.in/yaml/v3"
)

// UpdateRemote atomically adds, replaces or removes one remote in an existing
// user config. A nil value removes the entry. The sidecar lock serializes bp
// writers; unrelated keys and YAML comments remain in the same document.
func UpdateRemote(path, name string, value *RemoteConfig) error {
	if path == "" {
		return fmt.Errorf("config path is required")
	}
	if value != nil {
		candidate := *value
		if candidate.Transport == "" {
			candidate.Transport = "ssh"
		}
		if err := validateRemotes(map[string]RemoteConfig{name: candidate}); err != nil {
			return err
		}
		value = &candidate
	} else if !validFederationName(name) {
		return fmt.Errorf("invalid server name %q", name)
	}

	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var encoded []byte
	if filepath.Ext(path) == ".json" {
		encoded, err = updateRemoteJSON(data, name, value)
	} else {
		encoded, err = updateRemoteYAML(data, name, value)
	}
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return atomicConfigWrite(path, encoded, info.Mode().Perm())
}

func updateRemoteJSON(data []byte, name string, value *RemoteConfig) ([]byte, error) {
	root := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(data)) != 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, err
		}
	}
	remotes := map[string]RemoteConfig{}
	if raw := root["remotes"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &remotes); err != nil {
			return nil, fmt.Errorf("remotes: %w", err)
		}
	}
	if value == nil {
		if _, ok := remotes[name]; !ok {
			return nil, fmt.Errorf("unknown remote: %s", name)
		}
		delete(remotes, name)
	} else {
		remotes[name] = *value
	}
	if len(remotes) == 0 {
		delete(root, "remotes")
	} else {
		raw, err := json.Marshal(remotes)
		if err != nil {
			return nil, err
		}
		root["remotes"] = raw
	}
	encoded, err := json.MarshalIndent(root, "", "  ")
	return append(encoded, '\n'), err
}

func updateRemoteYAML(data []byte, name string, value *RemoteConfig) ([]byte, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil && err != io.EOF {
		return nil, err
	}
	if len(document.Content) == 0 {
		document.Kind = yaml.DocumentNode
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config must be a YAML mapping")
	}
	root := document.Content[0]
	remotes, remoteIndex := mappingValue(root, "remotes")
	if remotes == nil {
		if value == nil {
			return nil, fmt.Errorf("unknown remote: %s", name)
		}
		remotes = &yaml.Node{Kind: yaml.MappingNode}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "remotes"}, remotes)
		remoteIndex = len(root.Content) - 2
	}
	if remotes.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("remotes must be a mapping")
	}
	existing, index := mappingValue(remotes, name)
	if value == nil {
		if existing == nil {
			return nil, fmt.Errorf("unknown remote: %s", name)
		}
		remotes.Content = append(remotes.Content[:index], remotes.Content[index+2:]...)
		if len(remotes.Content) == 0 {
			root.Content = append(root.Content[:remoteIndex], root.Content[remoteIndex+2:]...)
		}
	} else {
		var encoded yaml.Node
		if err := encoded.Encode(value); err != nil {
			return nil, err
		}
		if existing == nil {
			remotes.Content = append(remotes.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, &encoded)
		} else {
			remotes.Content[index+1] = &encoded
		}
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, int) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1], index
		}
	}
	return nil, -1
}

func atomicConfigWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
