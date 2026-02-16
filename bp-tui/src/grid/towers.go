package grid

import (
	"sort"

	"bp-tui/src/tower/blueprint/history/20260118-1259-f14a-2026-01-18/impl"
)

// SetTower places a tower at the given position.
func SetTower(g Grid, pos Position, t tower.Tower) Grid {
	towers := cloneTowers(g.Towers)
	towers[pos] = t
	g.Towers = towers
	return g
}

// GetTower returns the tower at the given position, if any.
func GetTower(g Grid, pos Position) (tower.Tower, bool) {
	if g.Towers == nil {
		return tower.Tower{}, false
	}
	t, ok := g.Towers[pos]
	return t, ok
}

// RemoveTower removes any tower at the given position.
func RemoveTower(g Grid, pos Position) Grid {
	if g.Towers == nil {
		return g
	}
	towers := cloneTowers(g.Towers)
	delete(towers, pos)
	g.Towers = towers
	return g
}

// SetTowerLayers updates the layer count for the tower at pos.
func SetTowerLayers(g Grid, pos Position, layers int) Grid {
	if g.Towers == nil {
		return g
	}
	t, ok := g.Towers[pos]
	if !ok {
		return g
	}
	t = tower.SetLayers(t, layers)
	towers := cloneTowers(g.Towers)
	towers[pos] = t
	g.Towers = towers
	return g
}

// TowersInViewport returns positions of towers visible in the viewport.
func TowersInViewport(g Grid) []Position {
	if g.Towers == nil {
		return nil
	}
	if g.Viewport.Width <= 0 || g.Viewport.Height <= 0 {
		return nil
	}
	minX := g.Viewport.Offset.X
	minY := g.Viewport.Offset.Y
	maxX := minX + g.Viewport.Width - 1
	maxY := minY + g.Viewport.Height - 1

	positions := make([]Position, 0, len(g.Towers))
	for pos := range g.Towers {
		if pos.X < minX || pos.X > maxX || pos.Y < minY || pos.Y > maxY {
			continue
		}
		positions = append(positions, pos)
	}

	sort.Slice(positions, func(i, j int) bool {
		if positions[i].Y == positions[j].Y {
			return positions[i].X < positions[j].X
		}
		return positions[i].Y < positions[j].Y
	})

	return positions
}

func cloneTowers(src map[Position]tower.Tower) map[Position]tower.Tower {
	if src == nil {
		return map[Position]tower.Tower{}
	}
	dst := make(map[Position]tower.Tower, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
