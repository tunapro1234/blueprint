package input

import "strings"

// Action represents a user intent derived from key presses.
type Action int

const (
	None Action = iota
	MoveUp
	MoveDown
	MoveLeft
	MoveRight
	DrillDown
	DrillUp
	ToggleFlatten
	LayerDown
	LayerUp
	ZoomIn
	ZoomOut
	Quit
)

// KeyToAction maps a key string to an Action.
func KeyToAction(key string) Action {
	switch strings.ToLower(key) {
	case "w":
		return MoveUp
	case "s":
		return MoveDown
	case "a":
		return MoveLeft
	case "d":
		return MoveRight
	case "j":
		return DrillDown
	case "k":
		return DrillUp
	case "f":
		return ToggleFlatten
	case "h":
		return LayerDown
	case "l":
		return LayerUp
	case "up":
		return ZoomIn
	case "down":
		return ZoomOut
	case "q":
		return Quit
	default:
		return None
	}
}
