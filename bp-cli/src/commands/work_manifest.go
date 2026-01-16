package commands

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	bp "blueprint"
)

func readWorkManifest(path string) (WorkSession, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return WorkSession{}, err
	}
	parsed, err := bp.ParseYAML(data)
	if err != nil {
		return WorkSession{}, err
	}
	session := WorkSession{}
	session.ID = getStringValue(parsed, "id")
	session.Created = getStringValue(parsed, "created")
	session.Status = getStringValue(parsed, "status")
	pkgsRaw, ok := parsed["packages"].([]interface{})
	if ok {
		for _, raw := range pkgsRaw {
			m := toStringMap(raw)
			if m == nil {
				continue
			}
			pkg := WorkPackage{
				Path:          getStringValue(m, "path"),
				WorkPath:      getStringValue(m, "work_path"),
				Reason:        getStringValue(m, "reason"),
				BlueprintHash: getStringValue(m, "blueprint_hash"),
			}
			session.Packages = append(session.Packages, pkg)
		}
	}
	return session, nil
}

func writeWorkManifest(path string, session WorkSession) error {
	content := marshalWorkManifest(session)
	return os.WriteFile(path, []byte(content), 0o644)
}

func marshalWorkManifest(session WorkSession) string {
	var b strings.Builder
	b.WriteString("id: ")
	b.WriteString(yamlQuote(session.ID))
	b.WriteString("\ncreated: ")
	b.WriteString(yamlQuote(session.Created))
	b.WriteString("\nstatus: ")
	b.WriteString(yamlQuote(session.Status))
	b.WriteString("\npackages:\n")
	for _, pkg := range session.Packages {
		b.WriteString("  - path: ")
		b.WriteString(yamlQuote(pkg.Path))
		if pkg.WorkPath != "" {
			b.WriteString("\n    work_path: ")
			b.WriteString(yamlQuote(pkg.WorkPath))
		}
		b.WriteString("\n    reason: ")
		b.WriteString(yamlQuote(pkg.Reason))
		b.WriteString("\n    blueprint_hash: ")
		b.WriteString(yamlQuote(pkg.BlueprintHash))
		b.WriteString("\n")
	}
	return b.String()
}

func yamlQuote(value string) string {
	return strconv.Quote(value)
}

func toStringMap(raw interface{}) map[string]interface{} {
	switch m := raw.(type) {
	case map[string]interface{}:
		return m
	case map[interface{}]interface{}:
		out := map[string]interface{}{}
		for k, v := range m {
			out[fmt.Sprint(k)] = v
		}
		return out
	default:
		return nil
	}
}

func getStringValue(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
