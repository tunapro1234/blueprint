package world

import (
	"strings"

	grid "bp-tui/src/grid/blueprint/history/20260118-1301-6465-2026-01-18/impl"
	tower "bp-tui/src/tower/blueprint/history/20260118-1259-f14a-2026-01-18/impl"
)

type Position = grid.Position
type Viewport = grid.Viewport

// HierTower represents a tower that may contain a child grid.
type HierTower struct {
	Name          string
	Layers        int
	Path          string
	SelectedLayer int
	Children      *HierGrid
}

// HierGrid is a grid that can nest inside a tower.
type HierGrid struct {
	Towers   map[Position]HierTower
	Cursor   Position
	Viewport Viewport
	Parent   *HierGrid
}

// World holds the root grid and navigation state.
type World struct {
	Root       *HierGrid
	Current    *HierGrid
	Breadcrumb []string
	Flattened  bool
	Zoom       int
}

// NewWorld creates an empty world with a root grid.
func NewWorld(viewportW, viewportH int) World {
	root := &HierGrid{
		Towers: map[Position]HierTower{},
		Cursor: Position{X: 0, Y: 0},
		Viewport: Viewport{
			Offset: Position{X: 0, Y: 0},
			Width:  viewportW,
			Height: viewportH,
		},
	}
	return World{Root: root, Current: root, Zoom: DefaultZoomStep}
}

// AddTower adds a tower to the current grid.
func AddTower(w World, pos Position, t HierTower) World {
	if w.Current == nil {
		return w
	}
	if w.Current.Towers == nil {
		w.Current.Towers = map[Position]HierTower{}
	}
	if t.Layers < 1 {
		t.Layers = 1
	}
	if t.Layers > tower.MaxLayers {
		t.Layers = tower.MaxLayers
	}
	if t.SelectedLayer <= 0 {
		t.SelectedLayer = t.Layers
	}
	if t.SelectedLayer > t.Layers {
		t.SelectedLayer = t.Layers
	}
	if t.Children != nil {
		t.Children.Parent = w.Current
	}
	w.Current.Towers[pos] = t
	return w
}

// DrillDown enters the tower at the cursor if it has children.
func DrillDown(w World) World {
	if w.Current == nil {
		return w
	}
	t, ok := w.Current.Towers[w.Current.Cursor]
	if !ok || t.Children == nil {
		return w
	}
	child := t.Children
	child.Parent = w.Current
	w.Current = child
	name := t.Name
	if name == "" {
		name = t.Path
	}
	if name != "" {
		w.Breadcrumb = append(w.Breadcrumb, name)
	}
	return w
}

// DrillUp returns to the parent grid.
func DrillUp(w World) World {
	if w.Current == nil || w.Current.Parent == nil {
		return w
	}
	w.Current = w.Current.Parent
	if len(w.Breadcrumb) > 0 {
		w.Breadcrumb = w.Breadcrumb[:len(w.Breadcrumb)-1]
	}
	return w
}

// ToggleFlatten toggles flattened view mode.
func ToggleFlatten(w World) World {
	w.Flattened = !w.Flattened
	return w
}

// LayerDown moves selected layer down for the tower at cursor.
func LayerDown(w World) World {
	if w.Current == nil {
		return w
	}
	t, ok := w.Current.Towers[w.Current.Cursor]
	if !ok {
		return w
	}
	if t.Layers < 1 {
		return w
	}
	if t.SelectedLayer <= 0 {
		t.SelectedLayer = 1
	}
	if t.SelectedLayer > t.Layers {
		t.SelectedLayer = t.Layers
	}
	if t.SelectedLayer > 1 {
		t.SelectedLayer--
	}
	w.Current.Towers[w.Current.Cursor] = t
	return w
}

// LayerUp moves selected layer up for the tower at cursor.
func LayerUp(w World) World {
	if w.Current == nil {
		return w
	}
	t, ok := w.Current.Towers[w.Current.Cursor]
	if !ok {
		return w
	}
	if t.Layers < 1 {
		return w
	}
	if t.SelectedLayer <= 0 {
		t.SelectedLayer = 1
	}
	if t.SelectedLayer > t.Layers {
		t.SelectedLayer = t.Layers
	}
	if t.SelectedLayer < t.Layers {
		t.SelectedLayer++
	}
	w.Current.Towers[w.Current.Cursor] = t
	return w
}

// ZoomIn increases the zoom level.
func ZoomIn(w World) World {
	w.Zoom = clampZoomStep(w.Zoom + ZoomStep)
	return w
}

// ZoomOut decreases the zoom level.
func ZoomOut(w World) World {
	w.Zoom = clampZoomStep(w.Zoom - ZoomStep)
	return w
}

// CurrentPath returns the current location as a path string.
func CurrentPath(w World) string {
	if len(w.Breadcrumb) == 0 {
		return "/"
	}
	return "/" + strings.Join(w.Breadcrumb, "/")
}

// MoveCursor moves the cursor in the current grid and adjusts viewport.
func MoveCursor(w World, dx, dy int) World {
	if w.Current == nil {
		return w
	}
	w.Current.Cursor.X += dx
	w.Current.Cursor.Y += dy
	w.Current.Viewport = adjustViewport(w.Current.Cursor, w.Current.Viewport)
	return w
}

// ResizeViewport updates the current grid viewport size and keeps cursor visible.
func ResizeViewport(w World, width, height int) World {
	if w.Current == nil {
		return w
	}
	vp := w.Current.Viewport
	vp.Width = width
	vp.Height = height
	w.Current.Viewport = adjustViewport(w.Current.Cursor, vp)
	return w
}

func adjustViewport(cursor Position, vp Viewport) Viewport {
	if vp.Width <= 0 || vp.Height <= 0 {
		return vp
	}
	if cursor.X < vp.Offset.X {
		vp.Offset.X = cursor.X
	}
	if cursor.Y < vp.Offset.Y {
		vp.Offset.Y = cursor.Y
	}
	maxX := vp.Offset.X + vp.Width - 1
	maxY := vp.Offset.Y + vp.Height - 1
	if cursor.X > maxX {
		vp.Offset.X = cursor.X - vp.Width + 1
	}
	if cursor.Y > maxY {
		vp.Offset.Y = cursor.Y - vp.Height + 1
	}
	return vp
}

const (
	MinZoomStep     = -8
	MaxZoomStep     = 2
	DefaultZoomStep = 0
	ZoomStep        = 1
)

func clampZoomStep(step int) int {
	if step < MinZoomStep {
		return MinZoomStep
	}
	if step > MaxZoomStep {
		return MaxZoomStep
	}
	return step
}
