package grid

import (
	"reflect"
	"testing"

	tower "bp-tui/src/tower/blueprint/history/20260118-1259-f14a-2026-01-18/impl"
)

func TestMoveCursorKeepsCursorInViewport(t *testing.T) {
	g := NewGrid(3, 3)
	g = MoveCursor(g, 5, -4)
	if !positionInViewport(g.Cursor, g.Viewport) {
		t.Fatalf("cursor not in viewport: %+v in %+v", g.Cursor, g.Viewport)
	}
}

func TestViewportScrollsWhenCursorExits(t *testing.T) {
	g := NewGrid(3, 3)
	g = MoveCursor(g, 3, 0)
	if g.Viewport.Offset.X != 1 {
		t.Fatalf("expected viewport offset x=1, got %d", g.Viewport.Offset.X)
	}
	g = MoveCursor(g, 0, 3)
	if g.Viewport.Offset.Y != 1 {
		t.Fatalf("expected viewport offset y=1, got %d", g.Viewport.Offset.Y)
	}
}

func TestSetTowerPlacesTower(t *testing.T) {
	g := NewGrid(3, 3)
	pos := Position{X: 1, Y: 1}
	want := tower.NewTower("a")
	g = SetTower(g, pos, want)
	got, ok := GetTower(g, pos)
	if !ok {
		t.Fatalf("expected tower at position")
	}
	if got.Name != want.Name || got.Layers != want.Layers {
		t.Fatalf("unexpected tower: %+v", got)
	}
}

func TestTowersInViewportReturnsOnlyVisible(t *testing.T) {
	g := NewGrid(3, 3)
	g = SetTower(g, Position{X: 0, Y: 0}, tower.NewTower("a"))
	g = SetTower(g, Position{X: 2, Y: 2}, tower.NewTower("b"))
	g = SetTower(g, Position{X: 3, Y: 3}, tower.NewTower("c"))
	g = SetTower(g, Position{X: -1, Y: 0}, tower.NewTower("d"))

	got := TowersInViewport(g)
	want := []Position{{X: 0, Y: 0}, {X: 2, Y: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TowersInViewport mismatch: got %+v, want %+v", got, want)
	}
}

func positionInViewport(pos Position, vp Viewport) bool {
	if vp.Width <= 0 || vp.Height <= 0 {
		return false
	}
	minX := vp.Offset.X
	minY := vp.Offset.Y
	maxX := minX + vp.Width - 1
	maxY := minY + vp.Height - 1
	return pos.X >= minX && pos.X <= maxX && pos.Y >= minY && pos.Y <= maxY
}
