package gui

import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/container"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"

// coinsIcon is two overlapping coins, the Material Design "toll" icon like
// the theme's own icons, recoloured with the theme.
var coinsIcon = theme.NewThemedResource(fyne.NewStaticResource("coins.svg", []byte(
	`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">`+
		`<path d="M15 4c-4.42 0-8 3.58-8 8s3.58 8 8 8 8-3.58 8-8-3.58-8-8-8zm0 14c-3.31 0-6-2.69-6-6s2.69-6 6-6 6 2.69 6 6-2.69 6-6 6z`+
		`M3 12c0-2.61 1.67-4.83 4-5.65V4.26C3.55 5.15 1 8.27 1 12s2.55 6.85 6 7.74v-2.09c-2.33-.82-4-3.04-4-5.65z"/></svg>`)))

// tabs puts the home view in the Home tab of a tab bar running down the left
// of the window, above the Coins, Transactions and Settings tabs, each shown
// as its icon over its name. Home is selected.
func (g *gui) tabs(home fyne.CanvasObject) *container.AppTabs {
	var tabs = container.NewAppTabs(
		container.NewTabItemWithIcon("Home", theme.HomeIcon(), home),
		container.NewTabItemWithIcon("Coins", coinsIcon, placeholder("Coins")),
		container.NewTabItemWithIcon("Transactions", theme.HistoryIcon(), placeholder("Transactions")),
		container.NewTabItemWithIcon("Settings", theme.SettingsIcon(), placeholder("Settings")),
	)
	tabs.SetTabLocation(container.TabLocationLeading)
	tabs.SelectIndex(0)
	return tabs
}

// placeholder is the page of a tab that has no content yet: its name, centred.
func placeholder(name string) fyne.CanvasObject {
	return container.NewCenter(widget.NewLabelWithStyle(name, fyne.TextAlignCenter, fyne.TextStyle{Bold: true}))
}
