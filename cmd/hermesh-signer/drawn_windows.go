package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/hermeshai/hermesh/internal/preview"
	"github.com/hermeshai/hermesh/internal/signer"
)

// drawnPanel asks the owner in a window the signer draws itself, in the
// design of the Mac's panel: what a batch signs, then Windows Hello. This
// elevated process draws it, so the user's programs that are not elevated
// can neither change what it shows nor click it. Should the window fail,
// the task dialog stands in (panel_windows.go).
type drawnPanel struct {
	w        words
	fallback *panel
	hello    func(windows.HWND, string) (bool, error) // tests stand in for Windows Hello
	dark     func() bool

	mu   sync.Mutex
	open *view // the window up now
}

func newDrawnPanel() *drawnPanel {
	p := newPanel()
	return &drawnPanel{w: p.w, fallback: p, hello: hello, dark: appsDark}
}

// Language is the cards' language: the panel's.
func (p *drawnPanel) Language() string { return p.w.lang }

func (p *drawnPanel) Approve(ctx context.Context, cards []preview.Card) (bool, error) {
	v := &view{p: p, cards: cards, focus: "deny"} // Enter denies: approving takes a deliberate choice
	stop := context.AfterFunc(ctx, v.cancel)
	defer stop()
	var err error
	onOwnThread(func() { err = v.show() })
	if err != nil {
		fmt.Fprintln(os.Stderr, "hermesh-signer: the panel:", err)
		return p.fallback.Approve(ctx, cards)
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return v.ok, v.err
}

// Failed shows why a batch was not signed, unless the owner declined it,
// and returns at once.
func (p *drawnPanel) Failed(err error) {
	if errors.Is(err, signer.ErrRefused) {
		return
	}
	fmt.Fprintln(os.Stderr, "hermesh-signer: not signed:", err)
	message, noHello := err.Error(), errors.Is(err, errNoHello)
	if noHello {
		message = p.w.NoHello
	}
	v := &view{p: p, failed: true, message: message, noHello: noHello, focus: "ok"}
	go onOwnThread(func() {
		if v.show() != nil {
			p.fallback.Failed(errors.New(message))
		}
	})
}

// Show brings the open window forward.
func (p *drawnPanel) Show() {
	p.mu.Lock()
	v := p.open
	p.mu.Unlock()
	if v != nil {
		procSetForegroundWindow.Call(v.window())
	}
}

// appsDark: apps are dark in Settings › Personalization › Colours.
func appsDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

// theme is the panel's colours, light or dark.
type theme struct {
	bg, fg, fg2, fg3, card, line, bar, accent, accentOff, btn, btnLine, warn, warnMark, danger rgb
	aurora                                                                                     []rgb
}

func themeOf(dark bool) theme {
	if dark {
		bg, card, btn := rgb(0x202024), rgb(0x2a2a2f), rgb(0x2c2c31)
		return theme{bg: bg, fg: 0xececf1, fg2: 0xa3a5b0, fg3: 0xc4c6d0, card: card, line: over(card, 0xffffff, .08), bar: 0x1a1a1d,
			accent: 0x5b79ff, accentOff: 0x36407a, btn: btn, btnLine: over(btn, 0xffffff, .14), warn: 0xffab3d, warnMark: 0xffab3d, danger: 0xff6670,
			aurora: []rgb{over(bg, 0x22e1c9, .2), over(bg, 0x3e7bff, .18), over(bg, 0x7b61ff, .22)}}
	}
	bg := rgb(0xf9f9fb)
	return theme{bg: bg, fg: 0x1b1c22, fg2: 0x5b5d66, fg3: 0x4b4d57, card: 0xffffff, line: over(0xffffff, 0, .1), bar: 0xf0f1f4,
		accent: 0x3d5bff, accentOff: 0xa7b4ff, btn: 0xffffff, btnLine: over(0xffffff, 0, .16), warn: 0xa45605, warnMark: 0xd0700a, danger: 0xd7333f,
		aurora: []rgb{over(bg, 0x0891b2, .14), over(bg, 0x2f6bf0, .12), over(bg, 0x6d3cf0, .14)}}
}

type rect struct{ x, y, w, h int }

func (r rect) has(x, y int) bool { return x >= r.x && y >= r.y && x < r.x+r.w && y < r.y+r.h }

// op draws a part of the panel; dy is how far the content has scrolled.
type op func(c *canvas, dy int)

// view is one window of the panel: a batch to approve, or why one failed.
type view struct {
	p     *drawnPanel
	cards []preview.Card
	// failed: the window tells why a batch was not signed.
	failed  bool
	message string
	noHello bool

	mu        sync.Mutex
	hwnd      uintptr
	cancelled bool
	helloOK   bool
	helloErr  error

	// On the window's thread only.
	dpi                   int
	th                    theme
	fonts                 map[string]font
	details, waiting      bool
	spin                  int
	focus, hover, pressed string
	scroll                int
	tracking              bool
	ok                    bool
	err                   error

	// The layout, in pixels: the client is w wide; the content, contentH
	// high, shows viewH of it above a footer footH high.
	w, contentH, viewH, footH int
	main, foot                op
	targets                   map[string]rect // in the content (main) or the client (footer)
	inMain                    map[string]bool
}

func (v *view) window() uintptr {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.hwnd
}

// cancel ends the window: the request went away.
func (v *view) cancel() {
	v.mu.Lock()
	v.cancelled = true
	h := v.hwnd
	v.mu.Unlock()
	if h != 0 {
		procPostMessage.Call(h, wmClose, 1, 0)
	}
}

func (v *view) px(n float64) int { return int(math.Round(n * float64(v.dpi) / 96)) }

const (
	wmDestroy     = 0x0002
	wmPaint       = 0x000F
	wmClose       = 0x0010
	wmEraseBkgnd  = 0x0014
	wmSetCursor   = 0x0020
	wmNCCreate    = 0x0081
	wmKeyDown     = 0x0100
	wmTimer       = 0x0113
	wmMouseMove   = 0x0200
	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
	wmMouseWheel  = 0x020A
	wmMouseLeave  = 0x02A3
	wmDPIChanged  = 0x02E0
	wmSettingChg  = 0x001A
	wmHello       = 0x8001     // WM_APP+1: Windows Hello answered
	panelStyle    = 0x00C80000 // WS_OVERLAPPED | WS_CAPTION | WS_SYSMENU: no resizing, no maximizing
	panelExStyle  = 0x00000008 // WS_EX_TOPMOST
)

var (
	procRegisterClassEx          = user32.NewProc("RegisterClassExW")
	procCreateWindowEx           = user32.NewProc("CreateWindowExW")
	procDefWindowProc            = user32.NewProc("DefWindowProcW")
	procGetMessage               = user32.NewProc("GetMessageW")
	procPostQuitMessage          = user32.NewProc("PostQuitMessage")
	procBeginPaint               = user32.NewProc("BeginPaint")
	procEndPaint                 = user32.NewProc("EndPaint")
	procInvalidateRect           = user32.NewProc("InvalidateRect")
	procGetDC                    = user32.NewProc("GetDC")
	procReleaseDC                = user32.NewProc("ReleaseDC")
	procLoadCursor               = user32.NewProc("LoadCursorW")
	procSetCursor                = user32.NewProc("SetCursor")
	procGetCursorPos             = user32.NewProc("GetCursorPos")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procScreenToClient           = user32.NewProc("ScreenToClient")
	procSetCapture               = user32.NewProc("SetCapture")
	procReleaseCapture           = user32.NewProc("ReleaseCapture")
	procTrackMouseEvent          = user32.NewProc("TrackMouseEvent")
	procGetKeyState              = user32.NewProc("GetKeyState")
	procSetTimer                 = user32.NewProc("SetTimer")
	procKillTimer                = user32.NewProc("KillTimer")
	procGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	procAdjustWindowRectExForDpi = user32.NewProc("AdjustWindowRectExForDpi")
	procSystemParametersInfo     = user32.NewProc("SystemParametersInfoW")
	procDestroyWindow            = user32.NewProc("DestroyWindow")
	procPostMessage              = user32.NewProc("PostMessageW")
	procDwmSetWindowAttribute    = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")

	classOnce sync.Once
	classErr  error
	viewsMu   sync.Mutex
	views     = map[uintptr]*view{} // by window, and by id while one is created
	nextView  uintptr
)

const className = "HermeshSignerPanel"

func registerClass() error {
	classOnce.Do(func() {
		name, _ := windows.UTF16PtrFromString(className)
		arrow, _, _ := procLoadCursor.Call(0, 32512) // IDC_ARROW
		wc := struct {
			size, style            uint32
			proc                   uintptr
			clsExtra, wndExtra     int32
			instance, icon, cursor uintptr
			background, menu, name uintptr
			iconSmall              uintptr
		}{proc: syscall.NewCallback(wndProc), cursor: arrow, name: uintptr(unsafe.Pointer(name))}
		wc.size = uint32(unsafe.Sizeof(wc))
		if r, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			classErr = err
		}
	})
	return classErr
}

// show makes the window and answers it until it goes; an error when it
// can't be made.
func (v *view) show() error {
	if !startGDIPlus() {
		return errors.New("GDI+ did not start")
	}
	if err := registerClass(); err != nil {
		return fmt.Errorf("the window class: %w", err)
	}
	viewsMu.Lock()
	nextView++
	id := nextView
	views[id] = v
	viewsMu.Unlock()
	defer func() {
		viewsMu.Lock()
		delete(views, id)
		viewsMu.Unlock()
	}()
	class, _ := windows.UTF16PtrFromString(className)
	title, _ := windows.UTF16PtrFromString(v.p.w.Title)
	h, _, err := procCreateWindowEx.Call(panelExStyle, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), panelStyle,
		0x80000000, 0x80000000, 0x80000000, 0x80000000, 0, 0, 0, id) // CW_USEDEFAULT
	if h == 0 {
		return fmt.Errorf("the window: %w", err)
	}
	v.mu.Lock()
	v.hwnd = h
	cancelled := v.cancelled
	v.mu.Unlock()
	v.p.mu.Lock()
	v.p.open = v
	v.p.mu.Unlock()
	defer func() {
		v.p.mu.Lock()
		if v.p.open == v {
			v.p.open = nil
		}
		v.p.mu.Unlock()
		v.freeFonts()
	}()
	if cancelled {
		v.finish(false, context.Canceled)
	} else {
		v.dpi = dpiOf(h)
		v.restyle()
		v.place()
		procSetForegroundWindow.Call(h)
	}
	var msg [48]byte // MSG
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0)
		if r == 0 || int32(r) == -1 {
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg[0])))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&msg[0])))
	}
}

func dpiOf(hwnd uintptr) int {
	if d, _, _ := procGetDpiForWindow.Call(hwnd); d != 0 {
		return int(d)
	}
	return 96
}

// restyle takes the theme, the fonts and the layout for the window's DPI.
func (v *view) restyle() {
	v.th = themeOf(v.p.dark())
	on := int32(0)
	if v.p.dark() {
		on = 1
	}
	procDwmSetWindowAttribute.Call(v.hwnd, 20, uintptr(unsafe.Pointer(&on)), 4) // DWMWA_USE_IMMERSIVE_DARK_MODE
	v.freeFonts()
	v.fonts = map[string]font{}
	dc, _, _ := procCreateCompatibleDC.Call(0)
	defer procDeleteDC.Call(dc)
	face := func(want, fallback string) string {
		f := newFont(want, 14, 400)
		defer procDeleteObject.Call(f.h)
		if strings.EqualFold(faceOf(dc, f), want) {
			return want
		}
		return fallback
	}
	ui, mono := face("Segoe UI Variable Text", "Segoe UI"), face("Cascadia Mono", "Consolas")
	for name, spec := range map[string]struct {
		face   string
		size   float64
		weight int
	}{
		"body": {ui, 14, 400}, "bold": {ui, 14, 600}, "small": {ui, 13, 400}, "caption": {ui, 12, 400}, "captionBold": {ui, 12, 600},
		"title": {ui, 20, 600}, "mono": {mono, 24, 600}, "monoSmall": {mono, 18, 600},
	} {
		f := newFont(spec.face, v.px(spec.size), spec.weight)
		f.height = v.px(spec.size)
		v.fonts[name] = f
	}
	v.layout()
}

func (v *view) freeFonts() {
	for _, f := range v.fonts {
		procDeleteObject.Call(f.h)
	}
	v.fonts = nil
}

// place sizes the window to the layout, centres it on the work area and
// shows it on top.
func (v *view) place() {
	r := windows.Rect{Right: int32(v.w), Bottom: int32(v.viewH + v.footH)}
	procAdjustWindowRectExForDpi.Call(uintptr(unsafe.Pointer(&r)), panelStyle, 0, panelExStyle, uintptr(v.dpi))
	var area windows.Rect
	procSystemParametersInfo.Call(0x0030, 0, uintptr(unsafe.Pointer(&area)), 0) // SPI_GETWORKAREA
	w, h := r.Right-r.Left, r.Bottom-r.Top
	x, y := area.Left+(area.Right-area.Left-w)/2, area.Top+(area.Bottom-area.Top-h)/2
	procSetWindowPos.Call(v.hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0x0040) // SWP_SHOWWINDOW
}

// maxClient is the most the panel's client may take of the work area.
func (v *view) maxClient() int {
	var area windows.Rect
	procSystemParametersInfo.Call(0x0030, 0, uintptr(unsafe.Pointer(&area)), 0)
	return int(area.Bottom-area.Top)*85/100 - v.px(40)
}

// layout

type block func(c *canvas, x, y, w int) (int, []op)

// layout places the content and the footer for the window's DPI.
func (v *view) layout() {
	dc, _, _ := procCreateCompatibleDC.Call(0)
	defer procDeleteDC.Call(dc)
	c := newCanvas(dc)
	defer c.close()
	v.targets, v.inMain = map[string]rect{}, map[string]bool{}
	v.w = v.px(520)
	var ops []op
	if v.failed {
		v.contentH, ops = v.errorContent(c)
	} else {
		v.contentH, ops = v.confirmContent(c)
	}
	v.main = func(c *canvas, dy int) {
		for _, o := range ops {
			o(c, dy)
		}
	}
	v.footH = v.px(64)
	v.viewH = min(v.contentH, v.maxClient()-v.footH)
	v.scroll = max(0, min(v.scroll, v.contentH-v.viewH))
	v.foot = v.footer(c)
}

var factLine = regexp.MustCompile(`^([^:：]{1,24})[:：]\s*(.*)$`)

func (v *view) confirmContent(c *canvas) (int, []op) {
	cards, th := v.cards, v.th
	own := len(cards) > 1
	for _, cd := range cards {
		own = own && cd.Own
	}
	var hero *preview.Card
	if own || len(cards) == 1 {
		hero = &cards[0]
	}
	x, w := v.px(32), v.px(456)
	var ops []op
	y := v.px(20)
	put := func(h int, o []op) {
		ops = append(ops, o...)
		y += h + v.px(18)
	}
	// The hero: the shield, what it signs, why.
	{
		var o []op
		top := y
		cx := x + w/2
		o = append(o, func(c *canvas, dy int) { v.shield(c, cx-v.px(20), top-dy) })
		h := v.px(40) + v.px(8)
		if own {
			h += v.para(c, &o, "caption", v.p.w.OwnCaption, x, top+h, w, 1.35, th.fg2, true) + v.px(8)
		}
		title := fmt.Sprintf(v.p.w.Several, len(cards))
		if hero != nil {
			title = hero.Title
		}
		h += v.para(c, &o, "title", title, x, top+h, w, 1.35, th.fg, true)
		if hero != nil && hero.Why != "" {
			h += v.px(8)
			h += v.para(c, &o, "body", hero.Why, x+(w-v.px(420))/2, top+h, v.px(420), 1.6, th.fg2, true)
		}
		put(h, o)
	}
	if hero != nil {
		if hero.Code != "" {
			put(v.code(c, hero.Code, true)(c, x, y, w))
		}
		lines := hero.Lines
		if own {
			lines = append([]string(nil), hero.Lines[:min(1, len(hero.Lines))]...)
			var along []string
			for _, cd := range cards[1:] {
				along = append(along, cd.Title)
			}
			lines = append(lines, v.p.w.Along+"："+strings.Join(along, "、"))
			if v.p.w.lang != "zh-CN" {
				lines[len(lines)-1] = v.p.w.Along + ": " + strings.Join(along, ", ")
			}
		}
		if len(lines) > 0 {
			put(v.facts(c, lines, x, y, w))
		}
		if hero.Warning != "" {
			put(v.warning(c, hero.Warning, x, y, w))
		}
		if own {
			label := v.p.w.Details + " ›"
			if v.details {
				label = v.p.w.HideDetails
			}
			lw, lh := c.width(v.fonts["small"], label), c.lineHeight(v.fonts["small"])
			r := rect{x, y, lw, lh}
			v.targets["details"], v.inMain["details"] = r, true
			o := []op{func(c *canvas, dy int) {
				c.text(v.fonts["small"], r.x, r.y-dy, label, th.accent)
				if v.focus == "details" {
					c.ring(r.x-v.px(4), r.y-dy-v.px(2), r.w+v.px(8), r.h+v.px(4), v.px(4), max(1, v.px(2)), th.accent)
				}
			}}
			put(lh, o)
			if v.details {
				for _, cd := range cards {
					h, o := v.cardBox(c, cd, false, x, y, w) // its code is above
					ops = append(ops, o...)
					y += h + v.px(8)
				}
				y += v.px(18) - v.px(8)
			}
		}
	} else {
		for _, cd := range cards {
			h, o := v.cardBox(c, cd, true, x, y, w)
			ops = append(ops, o...)
			y += h + v.px(8)
		}
		y += v.px(18) - v.px(8)
	}
	return y - v.px(18) + v.px(24), ops
}

// para lays s out wrapped in w at (x, y), each line lh times the font's
// size apart; centred or from the left. It reports the height.
func (v *view) para(c *canvas, ops *[]op, fontName, s string, x, y, w int, lh float64, color rgb, center bool) int {
	f := v.fonts[fontName]
	lines := c.wrap(f, s, w)
	step := int(math.Round(float64(f.height) * lh))
	glyph := c.lineHeight(f)
	for i, l := range lines {
		lx, ly := x, y+i*step+(step-glyph)/2
		if center {
			lx = x + (w-c.width(f, l))/2
		}
		*ops = append(*ops, func(c *canvas, dy int) { c.text(f, lx, ly-dy, l, color) })
	}
	return len(lines) * step
}

// code is a verification code in tiles on the aurora, with what to do
// with it under (big), or alone (in a card).
func (v *view) code(c *canvas, code string, big bool) block {
	return func(c *canvas, x, y, w int) (int, []op) {
		th := v.th
		groups := strings.FieldsFunc(code, func(r rune) bool { return r == '-' || r == ' ' })
		pad, gap := v.px(10), v.px(8)
		f, tilePad := v.fonts["mono"], v.px(10)
		if !big {
			f, tilePad = v.fonts["monoSmall"], v.px(6)
		}
		tileH := 2*tilePad + c.lineHeight(f)
		h := pad + tileH + pad
		footY := y + pad + tileH + gap
		if big {
			h += gap + c.lineHeight(v.fonts["caption"])
		}
		n := max(len(groups), 1)
		tw := (w - 2*pad - (n-1)*gap) / n
		extra := v.px(2)
		var o []op
		o = append(o, func(c *canvas, dy int) { c.gradient(x, y-dy, w, h, v.px(12), th.aurora) })
		for i, g := range groups {
			tx := x + pad + i*(tw+gap)
			o = append(o, func(c *canvas, dy int) {
				c.round(tx, y+pad-dy+max(1, v.px(1)), tw, tileH, v.px(8), over(th.aurora[1], 0, .08)) // its shadow
				c.round(tx, y+pad-dy, tw, tileH, v.px(8), th.card)
				procSetTextCharacterExtra.Call(c.dc, uintptr(extra))
				gw := c.width(f, g)
				c.text(f, tx+(tw-gw+extra)/2, y+pad+tilePad-dy, g, th.fg)
				procSetTextCharacterExtra.Call(c.dc, 0)
			})
		}
		if big {
			cap, capBold := v.fonts["caption"], v.fonts["captionBold"]
			match := v.p.w.Match
			mw := c.width(capBold, match)
			o = append(o, func(c *canvas, dy int) {
				c.text(cap, x+pad, footY-dy, v.p.w.CodeLabel, th.fg3)
				c.text(capBold, x+w-pad-mw, footY-dy, match, th.fg)
			})
		}
		return h, o
	}
}

// facts are "label: value" lines as rows of a card.
func (v *view) facts(c *canvas, lines []string, x, y, w int) (int, []op) {
	th := v.th
	padY, padX, gap, bw := v.px(10), v.px(14), v.px(10), max(1, v.px(1))
	kw := v.px(72)
	type row struct{ k, val string }
	var rows []row
	for _, l := range lines {
		if m := factLine.FindStringSubmatch(l); m != nil {
			rows = append(rows, row{m[1], m[2]})
			kw = max(kw, c.width(v.fonts["body"], m[1]))
		} else {
			rows = append(rows, row{"", l})
		}
	}
	var o []op
	h := bw
	var seps []int
	for i, r := range rows {
		if i > 0 {
			seps = append(seps, y+h)
			h += bw
		}
		top := y + h + padY
		vx, vw := x+padX+kw+gap, w-2*padX-kw-gap
		if r.k == "" {
			vx, vw = x+padX, w-2*padX
		} else {
			v.para(c, &o, "body", r.k, x+padX, top, kw, 1.45, th.fg2, false)
		}
		rh := v.para(c, &o, "body", r.val, vx, top, vw, 1.45, th.fg, false)
		h += padY + rh + padY
	}
	h += bw
	frame := func(c *canvas, dy int) {
		c.framed(x, y-dy, w, h, v.px(10), bw, th.card, th.line)
		for _, s := range seps {
			c.rect(x+bw, s-dy, w-2*bw, bw, th.line)
		}
	}
	return h, append([]op{frame}, o...)
}

func (v *view) warning(c *canvas, text string, x, y, w int) (int, []op) {
	th := v.th
	var o []op
	h := v.para(c, &o, "small", text, x+v.px(24), y, w-v.px(24), 1.5, th.warn, false)
	mark := func(c *canvas, dy int) { v.warnMark(c, x, y+v.px(3)-dy) }
	return h, append([]op{mark}, o...)
}

// cardBox is one card of a batch: its title, its code, its lines.
func (v *view) cardBox(c *canvas, cd preview.Card, withCode bool, x, y, w int) (int, []op) {
	th := v.th
	padY, padX, bw := v.px(12), v.px(14), max(1, v.px(1))
	ix, iw := x+padX, w-2*padX
	var o []op
	h := bw + padY
	h += v.para(c, &o, "bold", cd.Title, ix, y+h, iw, 1.35, th.fg, false)
	if withCode && cd.Code != "" {
		h += v.px(10)
		ch, co := v.code(c, cd.Code, false)(c, ix, y+h, iw)
		o = append(o, co...)
		h += ch + v.px(6)
	}
	for _, l := range append(append([]string(nil), cd.Lines...), cd.Why) {
		if l == "" {
			continue
		}
		h += v.px(4)
		h += v.para(c, &o, "small", l, ix, y+h, iw, 1.5, th.fg2, false)
	}
	if cd.Warning != "" {
		h += v.px(6)
		wh, wo := v.warning(c, cd.Warning, ix, y+h, iw)
		o = append(o, wo...)
		h += wh
	}
	h += padY + bw
	frame := func(c *canvas, dy int) { c.framed(x, y-dy, w, h, v.px(10), bw, th.card, th.line) }
	return h, append([]op{frame}, o...)
}

func (v *view) errorContent(c *canvas) (int, []op) {
	th := v.th
	x, y := v.px(32), v.px(24)
	tx := x + v.px(32) + v.px(16)
	tw := v.w - tx - v.px(32)
	var o []op
	o = append(o, func(c *canvas, dy int) { v.errorMark(c, x, y-dy) })
	h := v.para(c, &o, "title", v.p.w.NotSigned, tx, y, tw, 1.35, th.fg, false)
	h += v.px(8)
	h += v.para(c, &o, "body", v.message, tx, y+h, tw, 1.6, th.fg3, false)
	return y + max(v.px(32), h) + v.px(24), o
}

// footer: a note at the left, the buttons at the right.
func (v *view) footer(c *canvas) op {
	th := v.th
	top, h := v.viewH, v.footH
	bh := v.px(32)
	by := top + (h-bh)/2
	type button struct {
		id, label string
		primary   bool
	}
	var buttons []button
	if v.failed {
		if v.noHello {
			buttons = append(buttons, button{"signin", v.p.w.OpenSignIn, false})
		}
		buttons = append(buttons, button{"ok", v.p.w.OK, true})
	} else {
		buttons = []button{{"deny", v.p.w.Deny, false}, {"approve", v.p.w.Approve, true}}
	}
	right := v.w - v.px(24)
	for i := len(buttons) - 1; i >= 0; i-- {
		b := buttons[i]
		f := v.fonts["body"]
		if b.primary {
			f = v.fonts["bold"]
		}
		bw := max(v.px(96), c.width(f, b.label)+v.px(32))
		right -= bw
		v.targets[b.id] = rect{right, by, bw, bh}
		right -= v.px(10)
	}
	noteX := v.px(24) + v.px(20) + v.px(10)
	return func(c *canvas, _ int) {
		c.rect(0, top, v.w, h, th.bar)
		c.rect(0, top, v.w, max(1, v.px(1)), th.line)
		if !v.failed {
			mx, my := v.px(24), top+(h-v.px(20))/2
			note := v.p.w.HelloAfter
			if v.waiting {
				v.spinner(c, mx+v.px(10), my+v.px(10))
				note = v.p.w.Wait
			} else {
				v.helloMark(c, mx, my)
			}
			f := v.fonts["small"]
			lines := c.wrap(f, note, right-noteX)
			if len(lines) > 0 {
				c.text(f, noteX, top+(h-c.lineHeight(f))/2, lines[0], th.fg3)
			}
		}
		for _, b := range buttons {
			v.button(c, v.targets[b.id], b.id, b.label, b.primary)
		}
	}
}

func (v *view) button(c *canvas, r rect, id, label string, primary bool) {
	th := v.th
	off := v.waiting
	hot := v.hover == id && !off
	f, color := v.fonts["body"], th.fg
	radius, bw := v.px(4), max(1, v.px(1))
	if primary {
		fill := th.accent
		switch {
		case off:
			fill = th.accentOff
		case v.pressed == id:
			fill = over(fill, 0, .16)
		case hot:
			fill = over(fill, 0, .08)
		}
		c.round(r.x, r.y, r.w, r.h, radius, fill)
		f, color = v.fonts["bold"], 0xffffff
	} else {
		fill := th.btn
		switch {
		case v.pressed == id && !off:
			fill = over(fill, th.fg, .08)
		case hot:
			fill = over(fill, th.fg, .04)
		}
		c.framed(r.x, r.y, r.w, r.h, radius, bw, fill, th.btnLine)
		if off {
			color = over(fill, th.fg, .45)
		}
	}
	c.text(f, r.x+(r.w-c.width(f, label))/2, r.y+(r.h-c.lineHeight(f))/2, label, color)
	if v.focus == id && !off {
		g := v.px(3)
		c.ring(r.x-g, r.y-g, r.w+2*g, r.h+2*g, radius+g, max(1, v.px(2)), th.accent)
	}
}

// marks, in their design's units (an SVG's), scaled

func (v *view) at(x, y int) func(a, b float64) [2]float64 {
	s := float64(v.dpi) / 96
	return func(a, b float64) [2]float64 { return [2]float64{float64(x) + a*s, float64(y) + b*s} }
}

func (v *view) shield(c *canvas, x, y int) {
	p := v.at(x, y)
	pt := func(a, b float64) point { q := p(a, b); return point{int32(math.Round(q[0])), int32(math.Round(q[1]))} }
	// M20 4 L33 9 V19 C33 27 27.5 33 20 36 C12.5 33 7 27 7 19 V9 Z, as a polygon of its curves.
	outline := []point{pt(20, 4), pt(33, 9), pt(33, 19)}
	bez := func(p0, p1, p2, p3 [2]float64) {
		for i := 1; i <= 12; i++ {
			t := float64(i) / 12
			u := 1 - t
			bx := u*u*u*p0[0] + 3*u*u*t*p1[0] + 3*u*t*t*p2[0] + t*t*t*p3[0]
			by := u*u*u*p0[1] + 3*u*u*t*p1[1] + 3*u*t*t*p2[1] + t*t*t*p3[1]
			outline = append(outline, pt(bx, by))
		}
	}
	bez([2]float64{33, 19}, [2]float64{33, 27}, [2]float64{27.5, 33}, [2]float64{20, 36})
	bez([2]float64{20, 36}, [2]float64{12.5, 33}, [2]float64{7, 27}, [2]float64{7, 19})
	outline = append(outline, pt(7, 9))
	c.polygon(outline, v.th.accent)
	c.stroke([][2]float64{p(14, 20), p(18.5, 24.5), p(26.5, 15.5)}, 2.6*float64(v.dpi)/96, 0xffffff)
}

func (v *view) warnMark(c *canvas, x, y int) {
	p := v.at(x, y)
	pt := func(a, b float64) point { q := p(a, b); return point{int32(math.Round(q[0])), int32(math.Round(q[1]))} }
	c.polygon([]point{pt(8, 1.8), pt(15, 14), pt(1, 14)}, v.th.warnMark)
	s := float64(v.dpi) / 96
	c.stroke([][2]float64{p(8, 6), p(8, 9.5)}, 1.4*s, 0xffffff)
	q := p(8, 11.7)
	c.dot(q[0], q[1], 0.8*s, 0xffffff, 255)
}

func (v *view) helloMark(c *canvas, x, y int) {
	s := float64(v.dpi) / 96
	p := v.at(x, y)
	col := v.th.fg2
	c.ring(x+v.px(2), y+v.px(2), v.px(16), v.px(16), v.px(4.5), max(1, v.px(1)), col)
	for _, e := range [][2]float64{p(7.5, 8.5), p(12.5, 8.5)} {
		c.dot(e[0], e[1], 1.1*s, col, 255)
	}
	var smile [][2]float64
	for i := 0; i <= 8; i++ {
		t := float64(i) / 8
		u := 1 - t
		// C8.6 13.6 11.4 13.6 13 12.2 from 7 12.2
		bx := u*u*u*7 + 3*u*u*t*8.6 + 3*u*t*t*11.4 + t*t*t*13
		by := u*u*u*12.2 + 3*u*u*t*13.6 + 3*u*t*t*13.6 + t*t*t*12.2
		smile = append(smile, p(bx, by))
	}
	c.stroke(smile, 1.1*s, col)
}

func (v *view) errorMark(c *canvas, x, y int) {
	s := float64(v.dpi) / 96
	p := v.at(x, y)
	o := p(16, 16)
	c.dot(o[0], o[1], 14*s, v.th.danger, 255)
	c.stroke([][2]float64{p(11, 11), p(21, 21)}, 2.4*s, 0xffffff)
	c.stroke([][2]float64{p(21, 11), p(11, 21)}, 2.4*s, 0xffffff)
}

// spinner: eight dots round (cx, cy), the brightest turning.
func (v *view) spinner(c *canvas, cx, cy int) {
	s := float64(v.dpi) / 96
	for i := range 8 {
		a := float64(i) * math.Pi / 4
		fade := uint8(255 - ((i-v.spin)%8+8)%8*28)
		c.dot(float64(cx)+7*s*math.Sin(a), float64(cy)-7*s*math.Cos(a), 1.6*s, v.th.accent, fade)
	}
}

// painting

func (v *view) paint(dc uintptr) {
	th := v.th
	c := newCanvas(dc)
	defer c.close()
	c.rect(0, 0, v.w, v.viewH, th.bg)
	c.clip(0, 0, v.w, v.viewH)
	v.main(c, v.scroll)
	if v.waiting { // the batch, dimmed while Windows Hello asks
		b := solid(th.bg, 115)
		procGdipFillRectangleI.Call(c.g, b, 0, 0, uintptr(v.w), uintptr(v.viewH))
		procGdipDeleteBrush.Call(b)
	}
	if v.contentH > v.viewH { // where in the content the view is
		bh := v.viewH * v.viewH / v.contentH
		by := v.scroll * (v.viewH - bh) / (v.contentH - v.viewH)
		c.round(v.w-v.px(7), by+v.px(2), v.px(3), bh-v.px(4), v.px(2), over(th.bg, th.fg2, .5))
	}
	c.unclip()
	v.foot(c, 0)
}

// render paints the client into a bitmap of its own and copies it in.
func (v *view) render(target uintptr) {
	h := v.viewH + v.footH
	dc, _, _ := procCreateCompatibleDC.Call(target)
	bmp, _, _ := procCreateCompatibleBitmap.Call(target, uintptr(v.w), uintptr(h))
	old, _, _ := procSelectObject.Call(dc, bmp)
	v.paint(dc)
	procBitBlt.Call(target, 0, 0, uintptr(v.w), uintptr(h), dc, 0, 0, 0x00CC0020) // SRCCOPY
	procSelectObject.Call(dc, old)
	procDeleteObject.Call(bmp)
	procDeleteDC.Call(dc)
}

// input

// hit is the target under (x, y) in the client.
func (v *view) hit(x, y int) string {
	for id, r := range v.targets {
		if v.inMain[id] {
			if y >= v.viewH || !(rect{r.x, r.y - v.scroll, r.w, r.h}).has(x, y) {
				continue
			}
			return id
		}
		if r.has(x, y) {
			return id
		}
	}
	return ""
}

// focusable: what Tab goes through, in order.
func (v *view) focusable() []string {
	if v.waiting {
		return nil
	}
	if v.failed {
		if v.noHello {
			return []string{"signin", "ok"}
		}
		return []string{"ok"}
	}
	if _, ok := v.targets["details"]; ok {
		return []string{"details", "deny", "approve"}
	}
	return []string{"deny", "approve"}
}

func (v *view) moveFocus(step int) {
	ids := v.focusable()
	if len(ids) == 0 {
		return
	}
	i := 0
	for j, id := range ids {
		if id == v.focus {
			i = j
		}
	}
	v.focus = ids[((i+step)%len(ids)+len(ids))%len(ids)]
	v.redraw()
}

func (v *view) activate(id string) {
	switch id {
	case "deny":
		v.finish(false, signer.ErrRefused)
	case "approve":
		v.approve()
	case "details":
		v.details = !v.details
		v.layout()
		v.place()
		v.redraw()
	case "ok":
		v.finish(false, nil)
	case "signin":
		windows.ShellExecute(0, nil, windows.StringToUTF16Ptr("ms-settings:signinoptions"), nil, nil, windows.SW_SHOWNORMAL)
		v.finish(false, nil)
	}
}

func (v *view) escape() {
	switch {
	case v.waiting:
	case v.failed:
		v.finish(false, nil)
	default:
		v.finish(false, signer.ErrRefused)
	}
}

// approve asks Windows Hello, the panel waiting under it.
func (v *view) approve() {
	v.waiting, v.hover, v.pressed = true, "", ""
	procSetTimer.Call(v.hwnd, 1, 90, 0)
	setTopmost(v.hwnd, false) // Windows Hello's window would be under it
	v.redraw()
	h := v.hwnd
	go func() {
		runtime.LockOSThread() // not unlocked: the thread, multithreaded for COM now, ends with this goroutine
		ok, err := v.p.hello(windows.HWND(h), v.p.w.Hello)
		v.mu.Lock()
		v.helloOK, v.helloErr = ok, err
		v.mu.Unlock()
		procPostMessage.Call(h, wmHello, 0, 0)
	}()
}

func (v *view) helloAnswered() {
	v.mu.Lock()
	ok, err := v.helloOK, v.helloErr
	v.mu.Unlock()
	procKillTimer.Call(v.hwnd, 1)
	switch {
	case err != nil:
		v.finish(false, err)
	case ok:
		v.finish(true, nil)
	default: // not verified: approve again, or deny
		v.waiting, v.focus = false, "deny"
		setTopmost(v.hwnd, true)
		v.redraw()
	}
}

func (v *view) finish(ok bool, err error) {
	v.ok, v.err = ok, err
	procDestroyWindow.Call(v.hwnd)
}

func (v *view) redraw() { procInvalidateRect.Call(v.hwnd, 0, 0) }

func setTopmost(hwnd uintptr, on bool) {
	after := hwndNotopmost
	if on {
		after = hwndTopmost
	}
	procSetWindowPos.Call(hwnd, after, 0, 0, 0, 0, swpNoMoveNoSize)
}

func wndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	viewsMu.Lock()
	v := views[hwnd]
	if v == nil && msg == wmNCCreate {
		id := *(*uintptr)(pointer(lparam)) // CREATESTRUCT.lpCreateParams
		if v = views[id]; v != nil {
			views[hwnd] = v
			v.mu.Lock()
			v.hwnd = hwnd
			v.mu.Unlock()
		}
	}
	viewsMu.Unlock()
	if v == nil || v.fonts == nil && msg != wmDestroy && msg != wmClose {
		return defProc(hwnd, msg, wparam, lparam)
	}
	x, y := int(int16(lparam)), int(int16(lparam>>16))
	switch msg {
	case wmPaint:
		var ps [72]byte // PAINTSTRUCT
		dc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps[0])))
		v.render(dc)
		procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps[0])))
		return 0
	case wmEraseBkgnd:
		return 1
	case wmMouseMove:
		if !v.tracking {
			tme := struct {
				size, flags uint32
				hwnd        uintptr
				hover       uint32
			}{flags: 0x2, hwnd: hwnd} // TME_LEAVE
			tme.size = uint32(unsafe.Sizeof(tme))
			procTrackMouseEvent.Call(uintptr(unsafe.Pointer(&tme)))
			v.tracking = true
		}
		if id := v.hit(x, y); id != v.hover {
			v.hover = id
			v.redraw()
		}
		return 0
	case wmMouseLeave:
		v.tracking, v.hover = false, ""
		v.redraw()
		return 0
	case wmLButtonDown:
		if v.waiting {
			return 0
		}
		v.pressed = v.hit(x, y)
		procSetCapture.Call(hwnd)
		v.redraw()
		return 0
	case wmLButtonUp:
		procReleaseCapture.Call()
		id := v.pressed
		v.pressed = ""
		v.redraw()
		if id != "" && id == v.hit(x, y) && !v.waiting {
			v.focus = id
			v.activate(id)
		}
		return 0
	case wmSetCursor:
		if lparam&0xffff == 1 { // HTCLIENT
			var pt [2]int32
			procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
			procScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
			cursor := uintptr(32512) // IDC_ARROW: on the buttons too, as Windows has it
			if v.hit(int(pt[0]), int(pt[1])) == "details" {
				cursor = 32649 // IDC_HAND: a link
			}
			h, _, _ := procLoadCursor.Call(0, cursor)
			procSetCursor.Call(h)
			return 1
		}
	case wmKeyDown:
		switch wparam {
		case 0x09: // Tab
			shift, _, _ := procGetKeyState.Call(0x10)
			if int16(shift) < 0 {
				v.moveFocus(-1)
			} else {
				v.moveFocus(1)
			}
		case 0x25, 0x26: // Left, Up
			v.moveFocus(-1)
		case 0x27, 0x28: // Right, Down
			v.moveFocus(1)
		case 0x0D, 0x20: // Enter, Space
			if !v.waiting && v.focus != "" {
				v.activate(v.focus)
			}
		case 0x1B: // Escape
			v.escape()
		}
		return 0
	case wmMouseWheel:
		if v.contentH > v.viewH {
			delta := int(int16(wparam >> 16))
			v.scroll = max(0, min(v.contentH-v.viewH, v.scroll-delta*v.px(48)/120))
			v.redraw()
		}
		return 0
	case wmTimer:
		v.spin++
		v.redraw()
		return 0
	case wmHello:
		v.helloAnswered()
		return 0
	case wmDPIChanged:
		v.dpi = int(wparam >> 16)
		v.restyle()
		v.place()
		v.redraw()
		return 0
	case wmSettingChg:
		if lparam != 0 && windows.UTF16PtrToString((*uint16)(pointer(lparam))) == "ImmersiveColorSet" {
			v.restyle()
			v.redraw()
		}
	case wmClose:
		if wparam == 1 { // the request went away
			v.finish(false, context.Canceled)
		} else {
			v.escape()
		}
		return 0
	case wmDestroy:
		viewsMu.Lock()
		delete(views, hwnd)
		viewsMu.Unlock()
		procPostQuitMessage.Call(0)
		return 0
	}
	return defProc(hwnd, msg, wparam, lparam)
}

// pointer is what a message's lParam points to.
func pointer(lparam uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&lparam)) }

func defProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	r, _, _ := procDefWindowProc.Call(hwnd, msg, wparam, lparam)
	return r
}
