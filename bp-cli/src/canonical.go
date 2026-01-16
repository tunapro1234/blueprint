package bp

import (
	"encoding/json"
	"sort"
	"strings"
)

func canonicalizeValue(value interface{}) string {
	switch v := value.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("{")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(jsonString(k))
			b.WriteString(":")
			b.WriteString(canonicalizeValue(v[k]))
		}
		b.WriteString("}")
		return b.String()
	case map[interface{}]interface{}:
		return canonicalizeValue(convertYAML(v))
	case []interface{}:
		var b strings.Builder
		b.WriteString("[")
		for i, item := range v {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(canonicalizeValue(item))
		}
		b.WriteString("]")
		return b.String()
	case []string:
		items := make([]interface{}, 0, len(v))
		for _, item := range v {
			items = append(items, item)
		}
		return canonicalizeValue(items)
	default:
		data, _ := json.Marshal(v)
		return string(data)
	}
}

func jsonString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}
