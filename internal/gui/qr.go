package gui

import "image"
import "image/color"
import "image/draw"
import "fyne.io/fyne/v2"
import "fyne.io/fyne/v2/canvas"
import "fyne.io/fyne/v2/widget"
import "github.com/skip2/go-qrcode"

// qrQuietZone is the number of blank modules around the QR symbol.
const qrQuietZone = 4

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

// CreateRenderer implements fyne.Widget.
func (q *QRWidget) CreateRenderer() fyne.WidgetRenderer {
	var r = &qrRenderer{widget: q}
	r.raster = canvas.NewRaster(q.generate)
	return r
}

// generate draws the QR modules into an image of size w×h with a standard
// quiet zone, centred in the available space.
func (q *QRWidget) generate(w, h int) image.Image {
	var img = image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.White, image.Point{}, draw.Src)
	var n = len(q.modules)
	if n == 0 || w == 0 || h == 0 { return img }
	var total = n + 2*qrQuietZone
	var scale = min(w, h)
	var cell = max(scale / total, 1)
	var ox = (w - total*cell) / 2
	var oy = (h - total*cell) / 2
	var black = image.NewUniform(color.RGBA{R: 0, G: 0, B: 0, A: 255})
	for y, row := range q.modules {
		for x, on := range row {
			if !on { continue }
			var r = image.Rect(
				ox+(x+qrQuietZone)*cell, oy+(y+qrQuietZone)*cell,
				ox+(x+qrQuietZone+1)*cell, oy+(y+qrQuietZone+1)*cell,
			)
			draw.Draw(img, r, black, image.Point{}, draw.Src)
		}
	}
	return img
}

type qrRenderer struct {
	widget *QRWidget
	raster *canvas.Raster
}

func (r *qrRenderer) Layout(size fyne.Size) { r.raster.Resize(size) }
func (r *qrRenderer) MinSize() fyne.Size    { return fyne.NewSquareSize(256) }
func (r *qrRenderer) Refresh()              { canvas.Refresh(r.raster) }
func (r *qrRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.raster}
}
func (r *qrRenderer) Destroy() {}
