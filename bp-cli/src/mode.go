package bp

import "strings"

// Mode returns the development mode for the blueprint (pussy | ro | hide).
// Defaults to "pussy" on missing or invalid values.
func (b *Blueprint) Mode() string {
	if b == nil || b.Data == nil {
		return "pussy"
	}
	metaRaw, ok := b.Data["_meta"]
	if !ok || metaRaw == nil {
		return "pussy"
	}
	meta, ok := convertYAML(metaRaw).(map[string]interface{})
	if !ok {
		return "pussy"
	}
	raw, ok := meta["mode"].(string)
	if !ok {
		return "pussy"
	}
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "pussy", "ro", "hide":
		return mode
	default:
		return "pussy"
	}
}
