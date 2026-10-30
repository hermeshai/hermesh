package main

import (
	"math"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The drawing the panel needs (drawn_windows.go): GDI+ for shapes, smooth
// at their edges, and GDI for text, in ClearType. Only GDI+ calls that take
// integers or pointers are used: Go passes a call's arguments in integer
// registers, where Windows on Arm doesn't look for a float.

var (
	gdi32                      = windows.NewLazySystemDLL("gdi32.dll")
	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procDeleteDC               = gdi32.NewProc("DeleteDC")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procCreateFont             = gdi32.NewProc("CreateFontW")
	procGetTextFace            = gdi32.NewProc("GetTextFaceW")
	procGetTextMetrics         = gdi32.NewProc("GetTextMetricsW")
	procGetTextExtentExPoint   = gdi32.NewProc("GetTextExtentExPointW")
	procGetTextExtentPoint32   = gdi32.NewProc("GetTextExtentPoint32W")
	procExtTextOut             = gdi32.NewProc("ExtTextOutW")
	procSetTextColor           = gdi32.NewProc("SetTextColor")
	procSetBkMode              = gdi32.NewProc("SetBkMode")
	procSetTextCharacterExtra  = gdi32.NewProc("SetTextCharacterExtra")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procIntersectClipRect      = gdi32.NewProc("IntersectClipRect")
	procSelectClipRgn          = gdi32.NewProc("SelectClipRgn")
	gdiplus                    = windows.NewLazySystemDLL("gdiplus.dll")
	procGdiplusStartup         = gdiplus.NewProc("GdiplusStartup")
	procGdipCreateFromHDC      = gdiplus.NewProc("GdipCreateFromHDC")
	procGdipDeleteGraphics     = gdiplus.NewProc("GdipDeleteGraphics")
	procGdipFlush              = gdiplus.NewProc("GdipFlush")
	procGdipSetSmoothingMode   = gdiplus.NewProc("GdipSetSmoothingMode")
	procGdipSetPixelOffsetMode = gdiplus.NewProc("GdipSetPixelOffsetMode")
	procGdipSetClipRectI       = gdiplus.NewProc("GdipSetClipRectI")
	procGdipResetClip          = gdiplus.NewProc("GdipResetClip")
	procGdipCreateSolidFill    = gdiplus.NewProc("GdipCreateSolidFill")
	procGdipCreateLineBrushI   = gdiplus.NewProc("GdipCreateLineBrushI")
	procGdipSetLinePresetBlend = gdiplus.NewProc("GdipSetLinePresetBlend")
	procGdipDeleteBrush        = gdiplus.NewProc("GdipDeleteBrush")
	procGdipCreatePath         = gdiplus.NewProc("GdipCreatePath")
	procGdipDeletePath         = gdiplus.NewProc("GdipDeletePath")
	procGdipAddPathLineI       = gdiplus.NewProc("GdipAddPathLineI")
	procGdipAddPathBezierI     = gdiplus.NewProc("GdipAddPathBezierI")
	procGdipClosePathFigure    = gdiplus.NewProc("GdipClosePathFigure")
	procGdipStartPathFigure    = gdiplus.NewProc("GdipStartPathFigure")
	procGdipFillPath           = gdiplus.NewProc("GdipFillPath")
	procGdipFillEllipseI       = gdiplus.NewProc("GdipFillEllipseI")
	procGdipFillPolygonI       = gdiplus.NewProc("GdipFillPolygonI")
	procGdipFillRectangleI     = gdiplus.NewProc("GdipFillRectangleI")
	gdiplusOnce                sync.Once
	gdiplusOK                  bool
)

// rgb is a colour as 0xRRGGBB; colorref is GDI's 0x00BBGGRR, argb GDI+'s.
type rgb uint32

func (c rgb) colorref() uintptr        { return uintptr(c>>16&0xff | c&0xff00 | (c&0xff)<<16) }
func (c rgb) argb(alpha uint8) uintptr { return uintptr(alpha)<<24 | uintptr(c) }

// over is c at alpha a (0..1) over the colour bg.
func over(bg, c rgb, a float64) rgb {
	ch := func(sh uint) rgb {
		x, y := float64(bg>>sh&0xff), float64(c>>sh&0xff)
		return rgb(math.Round(x*(1-a)+y*a)) << sh
	}
	return ch(16) | ch(8) | ch(0)
}

func startGDIPlus() bool {
	gdiplusOnce.Do(func() {
		input := struct {
			version                uint32
			debug                  uintptr
			noBackground, noCodecs int32
		}{version: 1}
		var token uintptr
		r, _, _ := procGdiplusStartup.Call(uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&input)), 0)
		gdiplusOK = r == 0
	})
	return gdiplusOK
}

type point struct{ x, y int32 }

// canvas draws on a memory DC: shapes through GDI+, text through GDI.
type canvas struct {
	dc uintptr
	g  uintptr // GDI+ graphics on dc
}

func newCanvas(dc uintptr) *canvas {
	c := &canvas{dc: dc}
	procGdipCreateFromHDC.Call(dc, uintptr(unsafe.Pointer(&c.g)))
	procGdipSetSmoothingMode.Call(c.g, 4)   // SmoothingModeAntiAlias
	procGdipSetPixelOffsetMode.Call(c.g, 4) // PixelOffsetModeHalf: pixel centres
	procSetBkMode.Call(dc, 1)               // TRANSPARENT
	return c
}

func (c *canvas) close() { procGdipDeleteGraphics.Call(c.g) }

// clip limits both GDI+ and GDI to the rectangle; unclip lifts it.
func (c *canvas) clip(x, y, w, h int) {
	procGdipSetClipRectI.Call(c.g, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0)
	procIntersectClipRect.Call(c.dc, uintptr(x), uintptr(y), uintptr(x+w), uintptr(y+h))
}

func (c *canvas) unclip() {
	procGdipResetClip.Call(c.g)
	procSelectClipRgn.Call(c.dc, 0)
}

func solid(color rgb, alpha uint8) uintptr {
	var b uintptr
	procGdipCreateSolidFill.Call(color.argb(alpha), uintptr(unsafe.Pointer(&b)))
	return b
}

func (c *canvas) rect(x, y, w, h int, color rgb) {
	b := solid(color, 255)
	procGdipFillRectangleI.Call(c.g, b, uintptr(x), uintptr(y), uintptr(w), uintptr(h))
	procGdipDeleteBrush.Call(b)
}

// roundPath is a rectangle with corners of radius r, as Bézier curves.
func roundPath(x, y, w, h, r int) uintptr {
	var p uintptr
	procGdipCreatePath.Call(0, uintptr(unsafe.Pointer(&p)))
	appendRound(p, x, y, w, h, r)
	return p
}

func (c *canvas) fillPath(p uintptr, color rgb, alpha uint8) {
	b := solid(color, alpha)
	procGdipFillPath.Call(c.g, b, p)
	procGdipDeleteBrush.Call(b)
	procGdipDeletePath.Call(p)
}

func (c *canvas) round(x, y, w, h, r int, color rgb) {
	c.fillPath(roundPath(x, y, w, h, r), color, 255)
}

// framed is a rounded rectangle with a border of width bw.
func (c *canvas) framed(x, y, w, h, r, bw int, fill, border rgb) {
	c.round(x, y, w, h, r, border)
	c.round(x+bw, y+bw, w-2*bw, h-2*bw, max(r-bw, 0), fill)
}

// ring is the outline of a rounded rectangle, width bw: the rectangle
// and the one inside it as two figures of one path, which the alternate
// fill leaves hollow.
func (c *canvas) ring(x, y, w, h, r, bw int, color rgb) {
	var p uintptr
	procGdipCreatePath.Call(0, uintptr(unsafe.Pointer(&p)))
	appendRound(p, x, y, w, h, r)
	appendRound(p, x+bw, y+bw, w-2*bw, h-2*bw, max(r-bw, 0))
	c.fillPath(p, color, 255)
}

func appendRound(p uintptr, x, y, w, h, r int) {
	procGdipStartPathFigure.Call(p)
	r = min(r, w/2, h/2)
	k := int(math.Round(float64(r) * 0.4477)) // where a quarter circle's control points sit
	R, B := x+w, y+h
	add := func(pts ...int) {
		if len(pts) == 4 {
			procGdipAddPathLineI.Call(p, uintptr(pts[0]), uintptr(pts[1]), uintptr(pts[2]), uintptr(pts[3]))
			return
		}
		procGdipAddPathBezierI.Call(p, uintptr(pts[0]), uintptr(pts[1]), uintptr(pts[2]), uintptr(pts[3]), uintptr(pts[4]), uintptr(pts[5]), uintptr(pts[6]), uintptr(pts[7]))
	}
	add(x+r, y, R-r, y)
	add(R-r, y, R-k, y, R, y+k, R, y+r)
	add(R, y+r, R, B-r)
	add(R, B-r, R, B-k, R-k, B, R-r, B)
	add(R-r, B, x+r, B)
	add(x+r, B, x+k, B, x, B-k, x, B-r)
	add(x, B-r, x, y+r)
	add(x, y+r, x, y+k, x+k, y, x+r, y)
	procGdipClosePathFigure.Call(p)
}

// gradient fills a rounded rectangle with colours along its diagonal.
func (c *canvas) gradient(x, y, w, h, r int, colors []rgb) {
	p1, p2 := point{int32(x), int32(y)}, point{int32(x + w), int32(y + h)}
	var b uintptr
	procGdipCreateLineBrushI.Call(uintptr(unsafe.Pointer(&p1)), uintptr(unsafe.Pointer(&p2)), colors[0].argb(255), colors[len(colors)-1].argb(255), 0, uintptr(unsafe.Pointer(&b)))
	argb := make([]uint32, len(colors))
	pos := make([]float32, len(colors))
	for i, col := range colors {
		argb[i] = uint32(col.argb(255))
		pos[i] = float32(i) / float32(len(colors)-1)
	}
	procGdipSetLinePresetBlend.Call(b, uintptr(unsafe.Pointer(&argb[0])), uintptr(unsafe.Pointer(&pos[0])), uintptr(len(colors)))
	p := roundPath(x, y, w, h, r)
	procGdipFillPath.Call(c.g, b, p)
	procGdipDeleteBrush.Call(b)
	procGdipDeletePath.Call(p)
}

func (c *canvas) dot(cx, cy, r float64, color rgb, alpha uint8) {
	b := solid(color, alpha)
	d := int(math.Round(2 * r))
	procGdipFillEllipseI.Call(c.g, b, uintptr(int(math.Round(cx-r))), uintptr(int(math.Round(cy-r))), uintptr(d), uintptr(d))
	procGdipDeleteBrush.Call(b)
}

func (c *canvas) polygon(pts []point, color rgb) {
	b := solid(color, 255)
	procGdipFillPolygonI.Call(c.g, b, uintptr(unsafe.Pointer(&pts[0])), uintptr(len(pts)), 0)
	procGdipDeleteBrush.Call(b)
}

// stroke draws a polyline of width w with round ends and joins.
func (c *canvas) stroke(pts [][2]float64, w float64, color rgb) {
	for i := 0; i+1 < len(pts); i++ {
		a, b := pts[i], pts[i+1]
		dx, dy := b[0]-a[0], b[1]-a[1]
		l := math.Hypot(dx, dy)
		if l == 0 {
			continue
		}
		nx, ny := -dy/l*w/2, dx/l*w/2
		at := func(x, y float64) point { return point{int32(math.Round(x)), int32(math.Round(y))} }
		c.polygon([]point{at(a[0]+nx, a[1]+ny), at(b[0]+nx, b[1]+ny), at(b[0]-nx, b[1]-ny), at(a[0]-nx, a[1]-ny)}, color)
	}
	for _, p := range pts {
		c.dot(p[0], p[1], w/2, color, 255)
	}
}

// fonts

type font struct {
	h      uintptr
	height int // the font's line, in pixels
}

func newFont(face string, px int, weight int) font {
	f, _ := windows.UTF16PtrFromString(face)
	h, _, _ := procCreateFont.Call(uintptr(-px), 0, 0, 0, uintptr(weight), 0, 0, 0, 1 /* DEFAULT_CHARSET */, 0, 0, 5 /* CLEARTYPE_QUALITY */, 0, uintptr(unsafe.Pointer(f)))
	return font{h: h}
}

// faceOf is the face Windows gave for f (another when f's isn't installed).
func faceOf(dc uintptr, f font) string {
	old, _, _ := procSelectObject.Call(dc, f.h)
	defer procSelectObject.Call(dc, old)
	buf := make([]uint16, 64)
	procGetTextFace.Call(dc, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	return windows.UTF16ToString(buf)
}

func (c *canvas) use(f font) {
	procSelectObject.Call(c.dc, f.h)
}

func (c *canvas) lineHeight(f font) int {
	c.use(f)
	var tm [64]byte // TEXTMETRICW; tmHeight first
	procGetTextMetrics.Call(c.dc, uintptr(unsafe.Pointer(&tm[0])))
	return int(*(*int32)(unsafe.Pointer(&tm[0])))
}

func (c *canvas) width(f font, s string) int {
	c.use(f)
	u := wide(s)
	if len(u) == 0 {
		return 0
	}
	var size [2]int32
	procGetTextExtentPoint32.Call(c.dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), uintptr(unsafe.Pointer(&size)))
	return int(size[0])
}

func (c *canvas) text(f font, x, y int, s string, color rgb) {
	c.use(f)
	u := wide(s)
	if len(u) == 0 {
		return
	}
	procGdipFlush.Call(c.g, 1) // FlushIntentionSync: the shapes under the text are down
	procSetTextColor.Call(c.dc, color.colorref())
	procExtTextOut.Call(c.dc, uintptr(x), uintptr(y), 0, 0, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), 0)
}

func wide(s string) []uint16 {
	u, _ := windows.UTF16FromString(s)
	return u[:len(u)-1]
}

// wrap breaks s into lines no wider than w: between words in Latin text,
// between any two characters of CJK text.
func (c *canvas) wrap(f font, s string, w int) []string {
	c.use(f)
	var lines []string
	for _, para := range splitLines(s) {
		rs := []rune(para)
		for len(rs) > 0 {
			u := wide(string(rs))
			var fit int32
			var size [2]int32
			procGetTextExtentExPoint.Call(c.dc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), uintptr(w), uintptr(unsafe.Pointer(&fit)), 0, uintptr(unsafe.Pointer(&size)))
			n := runesIn(rs, int(fit))
			if n >= len(rs) {
				lines = append(lines, string(rs))
				break
			}
			n = max(n, 1)
			// Not inside a word: back to the space before it, if there is one.
			if isWordRune(rs[n-1]) && isWordRune(rs[n]) {
				for i := n - 1; i > 0; i-- {
					if rs[i] == ' ' {
						n = i
						break
					}
					if !isWordRune(rs[i-1]) {
						n = i
						break
					}
				}
			}
			lines = append(lines, trimRight(string(rs[:n])))
			rs = rs[n:]
			for len(rs) > 0 && rs[0] == ' ' {
				rs = rs[1:]
			}
		}
		if len(rs) == 0 && para == "" {
			lines = append(lines, "")
		}
	}
	return lines
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i, r := range s {
		if r == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// runesIn is how many runes of rs take n UTF-16 units.
func runesIn(rs []rune, n int) int {
	units := 0
	for i, r := range rs {
		units++
		if r >= 0x10000 {
			units++
		}
		if units > n {
			return i
		}
	}
	return len(rs)
}

// isWordRune: a letter or digit of a script written with spaces between words.
func isWordRune(r rune) bool {
	return r < 0x2E80 && r != ' ' && r != '-' && r != '/'
}

func trimRight(s string) string {
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}
