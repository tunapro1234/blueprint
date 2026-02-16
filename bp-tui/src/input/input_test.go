package input

import "testing"

func TestKeyToActionMovement(t *testing.T) {
	cases := map[string]Action{
		"w": MoveUp,
		"W": MoveUp,
		"a": MoveLeft,
		"s": MoveDown,
		"d": MoveRight,
	}
	for key, want := range cases {
		if got := KeyToAction(key); got != want {
			t.Fatalf("KeyToAction(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestKeyToActionQuit(t *testing.T) {
	if got := KeyToAction("q"); got != Quit {
		t.Fatalf("KeyToAction(\"q\") = %v, want %v", got, Quit)
	}
}

func TestKeyToActionDrillDownUp(t *testing.T) {
	if got := KeyToAction("j"); got != DrillDown {
		t.Fatalf("KeyToAction(\"j\") = %v, want %v", got, DrillDown)
	}
	if got := KeyToAction("k"); got != DrillUp {
		t.Fatalf("KeyToAction(\"k\") = %v, want %v", got, DrillUp)
	}
}

func TestKeyToActionToggleFlatten(t *testing.T) {
	if got := KeyToAction("f"); got != ToggleFlatten {
		t.Fatalf("KeyToAction(\"f\") = %v, want %v", got, ToggleFlatten)
	}
}

func TestKeyToActionLayerSelect(t *testing.T) {
	if got := KeyToAction("h"); got != LayerDown {
		t.Fatalf("KeyToAction(\"h\") = %v, want %v", got, LayerDown)
	}
	if got := KeyToAction("l"); got != LayerUp {
		t.Fatalf("KeyToAction(\"l\") = %v, want %v", got, LayerUp)
	}
}

func TestKeyToActionZoom(t *testing.T) {
	if got := KeyToAction("up"); got != ZoomIn {
		t.Fatalf("KeyToAction(\"up\") = %v, want %v", got, ZoomIn)
	}
	if got := KeyToAction("down"); got != ZoomOut {
		t.Fatalf("KeyToAction(\"down\") = %v, want %v", got, ZoomOut)
	}
}

func TestKeyToActionUnknown(t *testing.T) {
	if got := KeyToAction("x"); got != None {
		t.Fatalf("KeyToAction(\"x\") = %v, want %v", got, None)
	}
}
