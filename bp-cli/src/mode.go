package bp

import "strings"

// Mode returns the development mode for the blueprint (pussy | ro | hide).
// Defaults to "ro" on missing or invalid values.
func (b *Blueprint) Mode() string {
	if b == nil || b.Data == nil {
		return "ro"
	}
	metaRaw, ok := b.Data["_meta"]
	if !ok || metaRaw == nil {
		return "ro"
	}
	meta, ok := convertYAML(metaRaw).(map[string]interface{})
	if !ok {
		return "ro"
	}
	raw, ok := meta["mode"].(string)
	if !ok {
		return "ro"
	}
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "pussy", "ro", "hide":
		return mode
	default:
		return "ro"
	}
}
