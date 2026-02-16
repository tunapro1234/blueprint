package tower

const MaxLayers = 6

// Tower represents a package stack with layers tied to snapshots.
type Tower struct {
	Name   string
	Layers int
}

// NewTower creates a tower with one layer.
func NewTower(name string) Tower {
	return Tower{Name: name, Layers: 1}
}

// AddLayer adds one layer if below MaxLayers.
func AddLayer(t Tower) Tower {
	if t.Layers < 1 {
		t.Layers = 1
	}
	if t.Layers < MaxLayers {
		t.Layers++
	}
	return t
}

// SetLayers clamps and sets the layer count to the 1..MaxLayers range.
func SetLayers(t Tower, n int) Tower {
	if n < 1 {
		n = 1
	} else if n > MaxLayers {
		n = MaxLayers
	}
	t.Layers = n
	return t
}
