package gui

import "testing"
import "fyne.io/fyne/v2/test"

// TestSwitch checks that tapping flips the switch and reports it, and that a
// disabled switch stays as it is.
func TestSwitch(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var changes []bool
	var s = NewSwitch(func(on bool) { changes = append(changes, on) })
	test.Tap(s)
	test.Tap(s)
	if s.On || len(changes) != 2 || !changes[0] || changes[1] {
		t.Fatalf("after two taps on = %v, changes %v; want off, [true false]", s.On, changes)
	}
	s.Disable()
	test.Tap(s)
	if s.On || len(changes) != 2 {
		t.Fatalf("disabled switch flipped: on = %v, changes %v", s.On, changes)
	}
}
