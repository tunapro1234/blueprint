package tower

import "testing"

func TestNewTowerCreatesOneLayer(t *testing.T) {
	tw := NewTower("pkg")
	if tw.Layers != 1 {
		t.Fatalf("expected 1 layer, got %d", tw.Layers)
	}
	if tw.Name != "pkg" {
		t.Fatalf("expected name to be set")
	}
}

func TestAddLayerIncrementsUpToMax(t *testing.T) {
	tw := NewTower("pkg")
	for i := 0; i < MaxLayers+3; i++ {
		tw = AddLayer(tw)
	}
	if tw.Layers != MaxLayers {
		t.Fatalf("expected layers to clamp at %d, got %d", MaxLayers, tw.Layers)
	}
}

func TestSetLayersClampsRange(t *testing.T) {
	tw := NewTower("pkg")
	if got := SetLayers(tw, -5).Layers; got != 1 {
		t.Fatalf("expected clamp to 1, got %d", got)
	}
	if got := SetLayers(tw, MaxLayers+5).Layers; got != MaxLayers {
		t.Fatalf("expected clamp to %d, got %d", MaxLayers, got)
	}
	if got := SetLayers(tw, 3).Layers; got != 3 {
		t.Fatalf("expected 3, got %d", got)
	}
}
