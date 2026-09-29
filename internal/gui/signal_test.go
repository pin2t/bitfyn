package gui

import "testing"

// TestBarSolid checks that the level counts solid bars from the left.
func TestBarSolid(t *testing.T) {
	for level := 0; level <= signalBars; level++ {
		var solid = 0
		for i := 0; i < signalBars; i++ {
			if barSolid(level, i) {
				solid++
				if i >= level {
					t.Errorf("level %d: bar %d solid beyond the level", level, i)
				}
			}
		}
		if solid != level {
			t.Errorf("level %d: %d solid bars", level, solid)
		}
	}
}

// TestBarHeightAscending checks that each bar is taller than the previous.
func TestBarHeightAscending(t *testing.T) {
	for i := 1; i < signalBars; i++ {
		if barHeight(i) <= barHeight(i-1) {
			t.Errorf("bar %d is not taller than bar %d", i, i-1)
		}
	}
}
