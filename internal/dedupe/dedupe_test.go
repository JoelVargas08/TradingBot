package dedupe

import "testing"

func TestDedupeSeen(t *testing.T) {
	d := New(10)
	if d.Seen("a") {
		t.Error("primera vez 'a' no debería ser seen")
	}
	if !d.Seen("a") {
		t.Error("segunda vez 'a' debería ser seen")
	}
	if d.Seen("b") {
		t.Error("'b' es clave distinta, no debería ser seen")
	}
}

func TestDedupeResetsOnLimit(t *testing.T) {
	d := New(2)
	if d.Seen("k1") || d.Seen("k2") {
		t.Fatal("k1/k2 no deberían ser seen la primera vez")
	}
	if d.Seen("k3") {
		t.Error("al resetear el mapa, 'k3' no debería ser seen la primera vez")
	}
	if !d.Seen("k3") {
		t.Error("tras agregar 'k3' en el nuevo mapa, debería ser seen la segunda vez")
	}
}
