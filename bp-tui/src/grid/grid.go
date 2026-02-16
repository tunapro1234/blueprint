package grid

import "bp-tui/src/tower/blueprint/history/20260118-1259-f14a-2026-01-18/impl"

// Position is a 2D coordinate in world space.
type Position struct {
	X int
	Y int
}

// Viewport describes the visible rectangle in world coordinates.
type Viewport struct {
	Offset Position
	Width  int
	Height int
}

// Grid holds cursor, viewport, and placed towers.
type Grid struct {
	Cursor   Position
	Viewport Viewport
	Towers   map[Position]tower.Tower
}

// NewGrid creates a grid with a viewport and no towers.
func NewGrid(width, height int) Grid {
	return Grid{
		Cursor: Position{X: 0, Y: 0},
		Viewport: Viewport{
			Offset: Position{X: 0, Y: 0},
			Width:  width,
			Height: height,
		},
		Towers: map[Position]tower.Tower{},
	}
}

// MoveCursor moves the cursor and scrolls the viewport if needed.
func MoveCursor(g Grid, dx, dy int) Grid {
	g.Cursor.X += dx
	g.Cursor.Y += dy
	g.Viewport = adjustViewport(g.Cursor, g.Viewport)
	return g
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
