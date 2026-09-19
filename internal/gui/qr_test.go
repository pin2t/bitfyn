package gui

import "image"
import "testing"

// TestRoundedCellMask checks that a cell mask cuts only the very corner of a
// large cell and keeps the centre and mid-edges fully opaque.
func TestRoundedCellMask(t *testing.T) {
	var m = roundedCellMask(8)
	if want := image.Rect(0, 0, 8, 8); m.Bounds() != want {
		t.Fatalf("bounds = %v, want %v", m.Bounds(), want)
	}
	if a := m.AlphaAt(0, 0).A; a != 0 {
		t.Errorf("corner alpha = %d, want 0", a)
	}
	for _, p := range []image.Point{{4, 4}, {1, 1}, {0, 4}, {4, 0}} {
		if a := m.AlphaAt(p.X, p.Y).A; a != 255 {
			t.Errorf("alpha at %v = %d, want 255", p, a)
		}
	}
}

// TestRoundedCellMaskSmall verifies that cells too small to round stay plain
// squares, so no module content is lost at low resolutions.
func TestRoundedCellMaskSmall(t *testing.T) {
	var m = roundedCellMask(3)
	for _, p := range []image.Point{{0, 0}, {1, 1}, {2, 2}} {
		if a := m.AlphaAt(p.X, p.Y).A; a != 255 {
			t.Errorf("alpha at %v = %d, want 255", p, a)
		}
	}
}
