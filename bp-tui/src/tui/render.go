package tui

import (
	"sort"
	"strings"

	world "bp-tui/src/world/blueprint/history/20260118-1738-790e-2026-01-18/impl"
)

const (
	AspectRatio  = 2
	BaseBoxHeight = 4
	MinBoxHeight  = 1
	MaxBoxHeight  = 6
	BottomBarRows = 6
	maxLayers     = 6
	layerOffsetX  = 2
	layerOffsetY  = 1
)

// CellDimensions returns the width and height of a grid cell.
func CellDimensions(zoom int) (int, int) {
	_, _, towerWidth, towerHeight := towerMetrics(zoom)
	cellWidth := towerWidth + 4
	cellHeight := towerHeight + 2
	return cellWidth, cellHeight
}

// RenderTower renders a tower as ASCII art.
func RenderTower(t world.HierTower, zoom int, selected bool) string {
	if t.Layers < 1 {
		return ""
	}

	layers := t.Layers
	if layers > maxLayers {
		layers = maxLayers
	}

	boxWidth, boxHeight, towerWidth, towerHeight := towerMetrics(zoom)

	canvas := make([][]rune, towerHeight)
	for i := range canvas {
		canvas[i] = make([]rune, towerWidth)
		for j := range canvas[i] {
			canvas[i][j] = ' '
		}
	}

	highlightLayer := -1
	if selected && layers >= 2 {
		selectedLayer := t.SelectedLayer
		if selectedLayer < 1 {
			selectedLayer = 1
		} else if selectedLayer > layers {
			selectedLayer = layers
		}
		highlightLayer = selectedLayer - 1
	}

	baseX := 0
	baseY := (maxLayers - 1) * layerOffsetY

	for depth := 0; depth < layers; depth++ {
		x := baseX + depth*layerOffsetX
		y := baseY - depth*layerOffsetY
		style := singleBorder
		if depth == highlightLayer {
			style = doubleBorder
		}
		drawLayer(canvas, x, y, boxWidth, boxHeight, style)
	}

	if t.Name != "" {
		displayName := t.Name
		if len(displayName) > boxWidth-2 {
			displayName = displayName[:boxWidth-2]
		}
		topX := baseX + (layers-1)*layerOffsetX
		topY := baseY - (layers-1)*layerOffsetY
		nameRow := topY + 1 + boxHeight/2
		nameCol := topX + 1 + (boxWidth-len(displayName))/2
		for i, ch := range displayName {
			canvas[nameRow][nameCol+i] = ch
		}
	}

	var sb strings.Builder
	for _, row := range canvas {
		sb.WriteString(string(row))
		sb.WriteRune('\n')
	}
	return sb.String()
}

// RenderGrid renders the entire grid viewport with all visible towers.
func RenderGrid(g world.HierGrid, zoom int) string {
	cellWidth, cellHeight := CellDimensions(zoom)
	_, _, _, towerHeight := towerMetrics(zoom)

	vp := g.Viewport
	if vp.Width <= 0 || vp.Height <= 0 {
		return ""
	}

	totalWidth := vp.Width * cellWidth
	totalHeight := vp.Height * cellHeight

	canvas := make([][]rune, totalHeight)
	for i := range canvas {
		canvas[i] = make([]rune, totalWidth)
		for j := range canvas[i] {
			canvas[i][j] = ' '
		}
	}

	// Draw dashed grid lines
	for cy := 0; cy <= vp.Height; cy++ {
		y := cy * cellHeight
		if y >= totalHeight {
			continue
		}
		for x := 0; x < totalWidth; x++ {
			if x%2 == 0 {
				canvas[y][x] = '┄'
			}
		}
	}
	for cx := 0; cx <= vp.Width; cx++ {
		x := cx * cellWidth
		if x >= totalWidth {
			continue
		}
		for y := 0; y < totalHeight; y++ {
			if y%2 == 0 {
				canvas[y][x] = '┆'
			}
		}
	}
	for cy := 0; cy <= vp.Height; cy++ {
		for cx := 0; cx <= vp.Width; cx++ {
			y := cy * cellHeight
			x := cx * cellWidth
			if y < totalHeight && x < totalWidth {
				canvas[y][x] = '┼'
			}
		}
	}

	// Highlight cursor cell
	cursorScreenX := (g.Cursor.X - vp.Offset.X) * cellWidth
	cursorScreenY := (g.Cursor.Y - vp.Offset.Y) * cellHeight
	if cursorScreenX >= 0 && cursorScreenY >= 0 {
		// Top border of cursor cell
		if cursorScreenY < totalHeight {
			for x := cursorScreenX; x < cursorScreenX+cellWidth && x < totalWidth; x++ {
				if canvas[cursorScreenY][x] == '┄' || canvas[cursorScreenY][x] == ' ' {
					canvas[cursorScreenY][x] = '━'
				} else if canvas[cursorScreenY][x] == '┼' {
					canvas[cursorScreenY][x] = '┿'
				}
			}
		}
		// Bottom border of cursor cell
		bottomY := cursorScreenY + cellHeight
		if bottomY < totalHeight {
			for x := cursorScreenX; x < cursorScreenX+cellWidth && x < totalWidth; x++ {
				if canvas[bottomY][x] == '┄' || canvas[bottomY][x] == ' ' {
					canvas[bottomY][x] = '━'
				} else if canvas[bottomY][x] == '┼' {
					canvas[bottomY][x] = '┿'
				}
			}
		}
		// Left border of cursor cell
		for y := cursorScreenY; y < cursorScreenY+cellHeight && y < totalHeight; y++ {
			if canvas[y][cursorScreenX] == '┆' || canvas[y][cursorScreenX] == ' ' {
				canvas[y][cursorScreenX] = '┃'
			}
		}
		// Right border of cursor cell
		rightX := cursorScreenX + cellWidth
		if rightX < totalWidth {
			for y := cursorScreenY; y < cursorScreenY+cellHeight && y < totalHeight; y++ {
				if canvas[y][rightX] == '┆' || canvas[y][rightX] == ' ' {
					canvas[y][rightX] = '┃'
				}
			}
		}
	}

	for _, pos := range towersInViewport(g) {
		t, ok := g.Towers[pos]
		if !ok {
			continue
		}

		screenX := (pos.X - vp.Offset.X) * cellWidth
		screenY := (pos.Y - vp.Offset.Y) * cellHeight

		// Bottom-left align tower in cell
		offsetX := 0
		offsetY := cellHeight - towerHeight

		selected := pos.X == g.Cursor.X && pos.Y == g.Cursor.Y

		towerStr := RenderTower(t, zoom, selected)
		towerLines := strings.Split(towerStr, "\n")

		for ty, line := range towerLines {
			runes := []rune(line)
			for tx, ch := range runes {
				cy := screenY + offsetY + ty
				cx := screenX + offsetX + tx
				if cy > 0 && cy < totalHeight-1 && cx > 0 && cx < totalWidth && ch != ' ' {
					canvas[cy][cx] = ch
				}
			}
		}
	}

	// Draw cursor marker if no tower at cursor
	_, hasTower := g.Towers[g.Cursor]
	if !hasTower {
		cx := cursorScreenX + cellWidth/2
		cy := cursorScreenY + cellHeight/2
		if cy > 0 && cy < totalHeight && cx > 0 && cx < totalWidth {
			canvas[cy][cx] = '◆'
		}
	}

	var sb strings.Builder
	for _, row := range canvas {
		line := strings.TrimRight(string(row), " ")
		sb.WriteString(line)
		sb.WriteRune('\n')
	}
	return sb.String()
}

type borderStyle struct {
	tl rune
	tr rune
	bl rune
	br rune
	h  rune
	v  rune
}

var singleBorder = borderStyle{
	tl: '┌',
	tr: '┐',
	bl: '└',
	br: '┘',
	h:  '─',
	v:  '│',
}

var doubleBorder = borderStyle{
	tl: '╔',
	tr: '╗',
	bl: '╚',
	br: '╝',
	h:  '═',
	v:  '║',
}

func drawLayer(canvas [][]rune, x, y, boxWidth, boxHeight int, style borderStyle) {
	width := boxWidth + 2
	height := boxHeight + 2
	for row := y; row < y+height; row++ {
		for col := x; col < x+width; col++ {
			canvas[row][col] = ' '
		}
	}

	canvas[y][x] = style.tl
	for i := 1; i <= boxWidth; i++ {
		canvas[y][x+i] = style.h
	}
	canvas[y][x+boxWidth+1] = style.tr

	for row := 1; row <= boxHeight; row++ {
		canvas[y+row][x] = style.v
		canvas[y+row][x+boxWidth+1] = style.v
	}

	canvas[y+boxHeight+1][x] = style.bl
	for i := 1; i <= boxWidth; i++ {
		canvas[y+boxHeight+1][x+i] = style.h
	}
	canvas[y+boxHeight+1][x+boxWidth+1] = style.br
}

func towersInViewport(g world.HierGrid) []world.Position {
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

	positions := make([]world.Position, 0, len(g.Towers))
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

// ViewportForWindow returns visible cell counts for the current window size.
func ViewportForWindow(width, height, zoom int) (cols, rows int) {
	cellWidth, cellHeight := CellDimensions(zoom)
	if cellWidth <= 0 || cellHeight <= 0 {
		return 0, 0
	}
	usableHeight := height - BottomBarRows
	if width <= 0 || usableHeight <= 0 {
		return 0, 0
	}
	return width / cellWidth, usableHeight / cellHeight
}

func towerMetrics(zoom int) (boxWidth, boxHeight, towerWidth, towerHeight int) {
	boxHeight = clampRange(BaseBoxHeight+zoom, MinBoxHeight, MaxBoxHeight)
	boxWidth = boxHeight * AspectRatio
	towerWidth = boxWidth + 2 + (maxLayers-1)*layerOffsetX
	towerHeight = boxHeight + 2 + (maxLayers-1)*layerOffsetY
	return
}

func clampRange(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
