package gui

import "image"
import "image/color"
import "image/draw"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/canvas"
import "fyne.io/fyne/v2/theme"
import "fyne.io/fyne/v2/widget"
import "github.com/skip2/go-qrcode"

// qrQuietZone is the number of blank modules between the symbol and the
// widget edge. The code is drawn flush with the widget; the window
// background around it acts as the quiet zone.
const qrQuietZone = 0

// qrCellPixels is the on-screen size of one QR module. The widget sizes
// itself to fit all modules at this size, keeping the code compact so the
// address text sits right under its bottom edge.
const qrCellPixels = 11

// QRWidget renders the QR code of a text payload as a crisp,
// resolution-independent raster that redraws at any widget size.
type QRWidget struct {
	widget.BaseWidget
	modules [][]bool
}

// NewQRWidget creates a QR widget for the given payload.
func NewQRWidget(content string) *QRWidget {
	var q = &QRWidget{}
	q.ExtendBaseWidget(q)
	_ = q.SetContent(content)
	return q
}

// SetContent replaces the encoded payload and refreshes the widget.
func (q *QRWidget) SetContent(content string) error {
	var code, err = qrcode.New(content, qrcode.Medium)
	if err != nil { return err }
	q.modules = code.Bitmap()
	q.Refresh()
	return nil
}

// CreateRenderer implements fyne.Widget. It subscribes to settings changes
// so the code re-renders in the new colours when the system switches between
// light and dark mode.
func (q *QRWidget) CreateRenderer() fyne.WidgetRenderer {
	var r = &qrRenderer{widget: q}
	r.raster = canvas.NewRaster(q.generate)
	var app = fyne.CurrentApp()
	if app != nil {
		app.Settings().AddListener(func(fyne.Settings) { q.Refresh() })
	}
	return r
}

// generate draws the QR modules into an image of size w×h in the current
// theme colours, with the quiet zone centred in the available space.
func (q *QRWidget) generate(w, h int) image.Image {
	var img = image.NewRGBA(image.Rect(0, 0, w, h))
	var background, foreground = qrColors()
	draw.Draw(img, img.Bounds(), image.NewUniform(background), image.Point{}, draw.Src)
	var n = len(q.modules)
	if n == 0 || w == 0 || h == 0 { return img }
	var total = n + 2*qrQuietZone
	var scale = min(w, h)
	var cell = max(scale / total, 1)
	var ox = (w - total*cell) / 2
	var oy = (h - total*cell) / 2
	var paint = image.NewUniform(foreground)
	var mask = roundedCellMask(cell)
	for y, row := range q.modules {
		for x, on := range row {
			if !on { continue }
			var r = image.Rect(
				ox+(x+qrQuietZone)*cell, oy+(y+qrQuietZone)*cell,
				ox+(x+qrQuietZone+1)*cell, oy+(y+qrQuietZone+1)*cell,
			)
			draw.DrawMask(img, r, paint, image.Point{}, mask, image.Point{}, draw.Over)
		}
	}
	return img
}

// qrColors returns the background and module colours of the current Fyne
// theme, so the QR code follows the light/dark system switch automatically.
func qrColors() (background, foreground color.Color) {
	if fyne.CurrentApp() == nil {
		return color.White, color.Black
	}
	return theme.Color(theme.ColorNameBackground), theme.Color(theme.ColorNameForeground)
}

// roundedCellMask builds an alpha mask for a single QR cell whose corners are
// slightly rounded, so the modules render as softened squares. Cells smaller
// than four pixels stay plain squares, keeping every module pixel at low
// resolutions. Only the corner circles are cut; elsewhere the cell is opaque.
func roundedCellMask(cell int) *image.Alpha {
	var mask = image.NewAlpha(image.Rect(0, 0, cell, cell))
	var r = cell / 4
	if r <= 0 {
		draw.Draw(mask, mask.Bounds(), image.White, image.Point{}, draw.Src)
		return mask
	}
	var last = cell - 1
	for y := 0; y < cell; y++ {
		for x := 0; x < cell; x++ {
			var dx = max(r-x, x-(last-r))
			var dy = max(r-y, y-(last-r))
			if dx <= 0 || dy <= 0 || dx*dx+dy*dy <= r*r {
				mask.SetAlpha(x, y, color.Alpha{A: 255})
			}
		}
	}
	return mask
}

type qrRenderer struct {
	widget *QRWidget
	raster *canvas.Raster
}

func (r *qrRenderer) Layout(size fyne.Size) { r.raster.Resize(size) }
func (r *qrRenderer) MinSize() fyne.Size {
	var total = len(r.widget.modules) + 2*qrQuietZone
	return fyne.NewSquareSize(float32(total * qrCellPixels))
}
func (r *qrRenderer) Refresh()              { canvas.Refresh(r.raster) }
func (r *qrRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.raster}
}
func (r *qrRenderer) Destroy() {}
