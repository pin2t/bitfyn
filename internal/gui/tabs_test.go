package gui

import "testing"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/test"
import "fyne.io/fyne/v2/theme"

// TestTabs checks the tab bar: down the left of the window, the tabs in
// order with their icons, and Home selected, showing the home view.
func TestTabs(t *testing.T) {
	var app = test.NewApp()
	defer app.Quit()
	var w = test.NewWindow(nil)
	defer w.Close()
	var g, err = newGUI(Options{DataDir: t.TempDir(), Network: "regtest"}, w)
	if err != nil {
		t.Fatalf("newGUI: %v", err)
	}
	defer g.store.Close()
	var home = g.content()
	var tabs = g.tabs(home)
	w.SetContent(tabs)
	w.Resize(fyne.NewSize(700, 800))
	var want = []struct {
		text string
		icon fyne.Resource
	}{
		{"Home", theme.HomeIcon()},
		{"Coins", coinsIcon},
		{"Transactions", theme.HistoryIcon()},
		{"Settings", theme.SettingsIcon()},
	}
	if len(tabs.Items) != len(want) {
		t.Fatalf("%d tabs, want %d", len(tabs.Items), len(want))
	}
	for i, tab := range want {
		if tabs.Items[i].Text != tab.text || tabs.Items[i].Icon != tab.icon {
			t.Errorf("tab %d is %q, want %q with its icon", i, tabs.Items[i].Text, tab.text)
		}
	}
	if tabs.SelectedIndex() != 0 || tabs.Selected().Content != home {
		t.Fatalf("selected tab %d, want Home with the home view", tabs.SelectedIndex())
	}
	var at = app.Driver().AbsolutePositionForObject(home)
	if !home.Visible() || at.X < 50 || at.Y > theme.Padding() {
		t.Fatalf("home view at %v, want at the top, right of the tab bar", at)
	}
}
