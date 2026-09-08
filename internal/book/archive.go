package book

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"
)

// Archived keeps the full registration in its original book. Native transcripts,
// launch claims and historical messages are never moved or rewritten.
type Archived struct {
	Agent Agent  `json:"agent"`
	Path  string `json:"agentbook_path"`
}

func Archives(paths []string) ([]Archived, error) {
	rows := []Archived{}
	for _, path := range Paths(paths) {
		file, err := Load(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, agent := range file.Agents {
			if agent.ArchivedAt != "" {
				rows = append(rows, Archived{agent, path})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Agent.Name < rows[j].Agent.Name })
	return rows, nil
}

func RequireUnarchived(paths []string, name string) error {
	rows, err := Archives(paths)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Agent.Name == name {
			return fmt.Errorf("%s is archived; use bp restore %s first", name, name)
		}
	}
	return nil
}

// SetArchived holds all configured book locks while checking hierarchy and
// updating one unambiguous record. check must only read external runtime/queue
// evidence; it must not write an agentbook or send terminal input.
func SetArchived(paths []string, name string, archive bool, check func(Agent) error) error {
	paths = Paths(paths)
	sort.Strings(paths)
	var locks []*os.File
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			syscall.Flock(int(locks[i].Fd()), syscall.LOCK_UN)
			locks[i].Close()
		}
	}()
	seen := map[string]bool{}
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			f.Close()
			return fmt.Errorf("agentbook in use; retry: %s", path)
		}
		locks = append(locks, f)
	}
	fleet, err := LoadFleet(paths)
	if err != nil {
		return err
	}
	if name == fleet.Root {
		return fmt.Errorf("cannot archive or replace the coordinator %s", name)
	}
	var target string
	var raw, record map[string]any
	var agent Agent
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var doc map[string]any
		if err = json.Unmarshal(data, &doc); err != nil {
			return err
		}
		rows, _ := doc["agents"].([]any)
		for _, value := range rows {
			row, ok := value.(map[string]any)
			if !ok || row["name"] != name {
				continue
			}
			if target != "" {
				return fmt.Errorf("%s has multiple registrations; resolve its source books before archiving/restoring", name)
			}
			target, raw, record = path, doc, row
			encoded, _ := json.Marshal(row)
			if err = json.Unmarshal(encoded, &agent); err != nil {
				return err
			}
		}
	}
	if target == "" {
		return fmt.Errorf("unknown agent: %s", name)
	}
	if archive == (agent.ArchivedAt != "") {
		return fmt.Errorf("%s is already %s", name, map[bool]string{true: "archived", false: "active"}[archive])
	}
	if archive {
		for child, parent := range fleet.Parents {
			if parent == name {
				return fmt.Errorf("%s still has child %s; archive or reparent children first", name, child)
			}
		}
	} else {
		parent := agent.Parent
		if parent == "" {
			parent, _ = raw["parent"].(string)
		}
		if parent == "" {
			parent = fleet.Root
		}
		if _, ok := fleet.Agents[parent]; !ok {
			return fmt.Errorf("restore parent %s before %s", parent, name)
		}
	}
	if check != nil {
		if err := check(agent); err != nil {
			return err
		}
	}
	if archive {
		record["archivedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	} else {
		delete(record, "archivedAt")
	}
	record["status"] = "closed"
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	return writeBook(target, raw, info.Mode().Perm())
}
