package bar

import (
	"fmt"
	"strings"
)

// BarData holds the data rendered in the bottom bar.
type BarData struct {
	Path          string
	Zoom          int
	Flattened     bool
	CursorX       int
	CursorY       int
	SelectedLayer int
	Metrics       Metrics
}

// Metrics holds placeholder values rendered in the bar.
type Metrics struct {
	TokensPerSecond  float64
	LinesPerMinute   int
	WaterUsagePerDay float64
	SnapshotsToday   int
}

// RenderBar returns a bar with a separator and fixed height.
func RenderBar(width int, height int, data BarData) string {
	if height <= 0 {
		return ""
	}

	separator := ""
	if width > 0 {
		separator = strings.Repeat("-", width)
	}

	layerText := "-"
	if data.SelectedLayer > 0 {
		layerText = fmt.Sprintf("%d", data.SelectedLayer)
	}

	content := []string{
		fmt.Sprintf("Path: %s", data.Path),
		fmt.Sprintf("Zoom: %+d | Flat: %v | Cursor: (%d,%d) | Layer: %s", data.Zoom, data.Flattened, data.CursorX, data.CursorY, layerText),
		fmt.Sprintf("Metrics: TPS %s | LPM %s", formatMetricFloat(data.Metrics.TokensPerSecond, 1), formatMetricInt(data.Metrics.LinesPerMinute)),
		fmt.Sprintf("Water/day: %s | Snapshots today: %s", formatMetricFloat(data.Metrics.WaterUsagePerDay, 1), formatMetricInt(data.Metrics.SnapshotsToday)),
		"Controls: WASD move | J/K drill | UP/DOWN zoom | F flatten | Q quit",
	}

	var sb strings.Builder
	sb.WriteString(trimWidth(separator, width))
	sb.WriteRune('\n')

	for i := 0; i < height-1; i++ {
		line := ""
		if i < len(content) {
			line = content[i]
		}
		sb.WriteString(trimWidth(line, width))
		sb.WriteRune('\n')
	}

	return sb.String()
}

func trimWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width])
}

func formatMetricFloat(value float64, decimals int) string {
	if value < 0 {
		return "--"
	}
	return fmt.Sprintf("%.*f", decimals, value)
}

func formatMetricInt(value int) string {
	if value < 0 {
		return "--"
	}
	return fmt.Sprintf("%d", value)
}
