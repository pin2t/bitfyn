package gui

import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/theme"

// headerLayout lays out the top of the window: the title centred on the
// first row with the connectivity indicator at its right, vertically centred
// on it, and the status line right under the indicator, ending at the right
// edge too, sharing its line with the network name centred under the title.
// The objects are the title, the indicator, the network name and the status.
type headerLayout struct{}

// MinSize is the widest line and the height down to the status line.
func (headerLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var title = objects[0].MinSize()
	var signal = objects[1].MinSize()
	var network = objects[2].MinSize()
	var status = objects[3].MinSize()
	var width = max(title.Width+2*signal.Width, network.Width+2*status.Width)
	return fyne.NewSize(width, statusTop(title, signal)+max(network.Height, status.Height))
}

// Layout places the title and the indicator on the first row and the status
// line under them from statusTop.
func (headerLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	var title = objects[0].MinSize()
	var signal = objects[1].MinSize()
	var network = objects[2].MinSize()
	var status = objects[3].MinSize()
	var row = max(title.Height, signal.Height)
	var top = statusTop(title, signal)
	objects[0].Resize(title)
	objects[0].Move(fyne.NewPos((size.Width-title.Width)/2, (row-title.Height)/2))
	objects[1].Resize(signal)
	objects[1].Move(fyne.NewPos(size.Width-signal.Width, (row-signal.Height)/2))
	objects[2].Resize(network)
	objects[2].Move(fyne.NewPos((size.Width-network.Width)/2, top))
	objects[3].Resize(status)
	objects[3].Move(fyne.NewPos(size.Width-status.Width, top))
}

// statusTop is the top of the status line: its label is raised into the
// space under the indicator by its own text padding, so its text starts one
// padding under the bars.
func statusTop(title, signal fyne.Size) float32 {
	var row = max(title.Height, signal.Height)
	var bars = (row + signal.Height) / 2
	return bars + theme.Padding() - theme.InnerPadding()
}
