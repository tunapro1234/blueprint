package bp

import "strings"

// Mode returns the development mode for the blueprint (meek | ro | hide).
// Defaults to "meek" on missing or invalid values.
func (b *Blueprint) Mode() string {
	if b == nil || b.Data == nil {
		return "meek"
	}
	metaRaw, ok := b.Data["_meta"]
	if !ok || metaRaw == nil {
		return "meek"
	}
	meta, ok := convertYAML(metaRaw).(map[string]interface{})
	if !ok {
		return "meek"
	}
	raw, ok := meta["mode"].(string)
	if !ok {
		return "meek"
	}
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "meek", "ro", "hide":
		return mode
	case "pussy":
		return "meek"
	default:
		return "meek"
	}
}
