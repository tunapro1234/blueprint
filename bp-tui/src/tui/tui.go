package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	bar "bp-tui/src/bar/blueprint/history/bar-metrics-placehol-2629"
	input "bp-tui/src/input/blueprint/history/20260118-1535-48c7-2026-01-18/impl"
	world "bp-tui/src/world/blueprint/history/20260118-1738-790e-2026-01-18/impl"
)

// Model holds the application state.
type Model struct {
	world  world.World
	bar    bar.BarData
	width  int
	height int
}

// NewModel creates a new model with sample towers.
func NewModel() Model {
	w := world.NewWorld(4, 3)

	w = world.AddTower(w, world.Position{X: 0, Y: 0}, world.HierTower{Name: "alpha", Layers: 2, Path: "alpha"})
	w = world.AddTower(w, world.Position{X: 2, Y: 0}, world.HierTower{Name: "beta", Layers: 4, Path: "beta"})
	w = world.AddTower(w, world.Position{X: 1, Y: 1}, world.HierTower{Name: "gamma", Layers: 1, Path: "gamma"})

	child := &world.HierGrid{
		Towers: map[world.Position]world.HierTower{
			{X: 0, Y: 0}: {Name: "delta", Layers: 3, Path: "delta"},
			{X: 1, Y: 0}: {Name: "epsilon", Layers: 2, Path: "epsilon"},
		},
		Cursor: world.Position{X: 0, Y: 0},
		Viewport: world.Viewport{
			Offset: world.Position{X: 0, Y: 0},
			Width:  3,
			Height: 2,
		},
	}
	w = world.AddTower(w, world.Position{X: 3, Y: 2}, world.HierTower{Name: "nested", Layers: 3, Path: "nested", Children: child})

	return Model{world: w, bar: barDataForWorld(w)}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		action := input.KeyToAction(msg.String())
		switch action {
		case input.Quit:
			return m, tea.Quit
		case input.MoveUp:
			m.world = world.MoveCursor(m.world, 0, -1)
		case input.MoveDown:
			m.world = world.MoveCursor(m.world, 0, 1)
		case input.MoveLeft:
			m.world = world.MoveCursor(m.world, -1, 0)
		case input.MoveRight:
			m.world = world.MoveCursor(m.world, 1, 0)
		case input.DrillDown:
			m.world = world.DrillDown(m.world)
			m = m.applyViewport()
		case input.DrillUp:
			m.world = world.DrillUp(m.world)
			m = m.applyViewport()
		case input.ToggleFlatten:
			m.world = world.ToggleFlatten(m.world)
		case input.LayerDown:
			m.world = world.LayerDown(m.world)
		case input.LayerUp:
			m.world = world.LayerUp(m.world)
		case input.ZoomIn:
			m.world = world.ZoomIn(m.world)
			m = m.applyViewport()
		case input.ZoomOut:
			m.world = world.ZoomOut(m.world)
			m = m.applyViewport()
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m = m.applyViewport()
	}

	m.bar = barDataForWorld(m.world)
	return m, nil
}

// View implements tea.Model.
func (m Model) View() string {
	grid := ""
	if m.world.Current != nil {
		grid = RenderGrid(*m.world.Current, m.world.Zoom)
	}
	return grid + bar.RenderBar(m.width, BottomBarRows, m.bar)
}

// Run starts the TUI application.
func Run() error {
	p := tea.NewProgram(NewModel(), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m Model) applyViewport() Model {
	if m.world.Current == nil || m.width <= 0 || m.height <= 0 {
		return m
	}
	cols, rows := ViewportForWindow(m.width, m.height, m.world.Zoom)
	m.world = world.ResizeViewport(m.world, cols, rows)
	return m
}

func barDataForWorld(w world.World) bar.BarData {
	cursorX, cursorY := 0, 0
	selectedLayer := 0
	if w.Current != nil {
		cursorX = w.Current.Cursor.X
		cursorY = w.Current.Cursor.Y
		if t, ok := w.Current.Towers[w.Current.Cursor]; ok {
			selectedLayer = t.SelectedLayer
		}
	}
	return bar.BarData{
		Path:          world.CurrentPath(w),
		Zoom:          w.Zoom,
		Flattened:     w.Flattened,
		CursorX:       cursorX,
		CursorY:       cursorY,
		SelectedLayer: selectedLayer,
		Metrics: bar.Metrics{
			TokensPerSecond:  -1,
			LinesPerMinute:   -1,
			WaterUsagePerDay: -1,
			SnapshotsToday:   -1,
		},
	}
}
