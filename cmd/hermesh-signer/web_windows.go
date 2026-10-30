package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"

	"github.com/hermeshai/hermesh/internal/preview"
	"github.com/hermeshai/hermesh/internal/signer"
)

// webPanel asks the owner in a window of the signer's own drawn by
// WebView2 (panel.html): the design of the Mac's panel, at the signer's
// high integrity, so programs of theirs that are not elevated can neither
// change nor click it. Its browser keeps its data where only
// administrators write (beside the signer, in Program Files). Without
// WebView2 the task dialog stands in (panel_windows.go).

//go:embed panel.html
var panelPage string

var (
	procGetDpiForWindow                  = user32.NewProc("GetDpiForWindow")
	procAdjustWindowRectExForDpi         = user32.NewProc("AdjustWindowRectExForDpi")
	procSetWindowLongPtr                 = user32.NewProc("SetWindowLongPtrW")
	procSystemParametersInfo             = user32.NewProc("SystemParametersInfoW")
	procShowWindow                       = user32.NewProc("ShowWindow")
	procDestroyWindow                    = user32.NewProc("DestroyWindow")
	procPostMessage                      = user32.NewProc("PostMessageW")
	procDwmSetWindowAttribute            = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
	procCoInitializeEx                   = windows.NewLazySystemDLL("ole32.dll").NewProc("CoInitializeEx")
	errNoWebView                         = errors.New("WebView2 is not available")
	errApartment                         = errors.New("this thread's COM is multithreaded")
	panelStyle                   uintptr = 0x00C80000 // WS_OVERLAPPED | WS_CAPTION | WS_SYSMENU: no resizing, no maximizing
)

type webPanel struct {
	w        words
	dataDir  string
	fallback *panel

	mu   sync.Mutex
	hwnd uintptr // the window open now
}

func newWebPanel(sid string) *webPanel {
	p := newPanel()
	exe, _ := os.Executable()
	return &webPanel{w: p.w, dataDir: filepath.Join(filepath.Dir(exe), "webview", sid), fallback: p}
}

// Language is the cards' language: the panel's.
func (p *webPanel) Language() string { return p.w.lang }

type outcome struct {
	ok  bool
	err error
}

func (p *webPanel) Approve(ctx context.Context, cards []preview.Card) (bool, error) {
	result := make(chan outcome, 2) // the first answer counts
	err := p.open(map[string]any{"kind": "confirm", "cards": cards}, func(v webview2.WebView, hwnd uintptr) {
		v.Bind("approve", func() {
			// Off the top while Windows Hello asks, whose window would be under it.
			setTopmost(hwnd, false)
			go func() {
				runtime.LockOSThread() // not unlocked: the thread, multithreaded for COM now, ends with this goroutine
				ok, err := hello(windows.HWND(hwnd), p.w.Hello)
				if !ok && err == nil { // not verified: approve again, or deny
					v.Dispatch(func() {
						setTopmost(hwnd, true)
						v.Eval("ready()")
					})
					return
				}
				result <- outcome{ok, err}
				v.Dispatch(func() { procDestroyWindow.Call(hwnd) })
			}()
		})
		v.Bind("deny", func() {
			result <- outcome{false, signer.ErrRefused}
			procDestroyWindow.Call(hwnd)
		})
	}, func() { result <- outcome{false, signer.ErrRefused} }) // closed
	if errors.Is(err, errNoWebView) {
		return p.fallback.Approve(ctx, cards)
	}
	if err != nil {
		return false, err
	}
	select {
	case r := <-result:
		return r.ok, r.err
	case <-ctx.Done():
		p.close()
		return false, ctx.Err()
	}
}

// Failed shows why a batch was not signed, unless the owner declined it,
// and returns at once.
func (p *webPanel) Failed(err error) {
	if errors.Is(err, signer.ErrRefused) {
		return
	}
	fmt.Fprintln(os.Stderr, "hermesh-signer: not signed:", err)
	message := err.Error()
	if errors.Is(err, errNoHello) {
		message = p.w.NoHello
	}
	go func() {
		err := p.open(map[string]any{"kind": "error", "title": p.w.NotSigned, "message": message, "hello": errors.Is(err, errNoHello)}, func(v webview2.WebView, hwnd uintptr) {
			v.Bind("done", func() { procDestroyWindow.Call(hwnd) })
			v.Bind("openSignIn", func() {
				windows.ShellExecute(0, nil, windows.StringToUTF16Ptr("ms-settings:signinoptions"), nil, nil, windows.SW_SHOWNORMAL)
				procDestroyWindow.Call(hwnd)
			})
		}, func() {})
		if errors.Is(err, errNoWebView) {
			p.fallback.Failed(errors.New(message))
		}
	}()
}

// Show brings the open window forward.
func (p *webPanel) Show() {
	p.mu.Lock()
	h := p.hwnd
	p.mu.Unlock()
	if h != 0 {
		procSetForegroundWindow.Call(h)
	}
}

func (p *webPanel) close() {
	p.mu.Lock()
	h := p.hwnd
	p.mu.Unlock()
	if h != 0 {
		procPostMessage.Call(h, 0x0010, 0, 0) // WM_CLOSE: the window's thread ends it
	}
}

// open shows panel.html with state in a window of its own, on a thread of
// its own, until the window goes; bind adds the page's calls, and closed
// runs when the window goes. It returns once the window is up.
func (p *webPanel) open(state map[string]any, bind func(v webview2.WebView, hwnd uintptr), closed func()) error {
	state["words"] = p.w.page()
	js, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.dataDir, 0o700); err != nil {
		return err
	}
	// WebView2 needs a thread whose COM is single-threaded. One that is
	// multithreaded already (left so by other code) would wait for it
	// forever, a blank window: that thread is ended, and another tried.
	for range 64 {
		if err := p.openOnThread(js, bind, closed); !errors.Is(err, errApartment) {
			return err
		}
	}
	return errNoWebView
}

func (p *webPanel) openOnThread(js []byte, bind func(v webview2.WebView, hwnd uintptr), closed func()) error {
	up := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // not unlocked: the thread and its COM end with the window
		// COINIT_APARTMENTTHREADED; RPC_E_CHANGED_MODE: multithreaded already.
		if hr, _, _ := procCoInitializeEx.Call(0, 0x2); hr == 0x80010106 {
			up <- errApartment
			return
		}
		v := webview2.NewWithOptions(webview2.WebViewOptions{DataPath: p.dataDir, WindowOptions: webview2.WindowOptions{Title: p.w.Title, Width: 520, Height: 400}})
		if v == nil {
			up <- errNoWebView
			return
		}
		hwnd := uintptr(v.Window())
		procShowWindow.Call(hwnd, 0)                              // SW_HIDE until the page has its height
		procSetWindowLongPtr.Call(hwnd, ^uintptr(15), panelStyle) // GWL_STYLE (-16)
		v.Bind("resize", func(height float64) { place(hwnd, int(height)) })
		v.Bind("theme", func(dark bool) {
			on := int32(0)
			if dark {
				on = 1
			}
			procDwmSetWindowAttribute.Call(hwnd, 20, uintptr(unsafe.Pointer(&on)), 4) // DWMWA_USE_IMMERSIVE_DARK_MODE
		})
		bind(v, hwnd)
		v.Init("window.__state = " + string(js) + ";")
		v.SetHtml(panelPage)
		p.mu.Lock()
		p.hwnd = hwnd
		p.mu.Unlock()
		up <- nil
		v.Run() // until the window goes (its WM_DESTROY ends the loop)
		p.mu.Lock()
		if p.hwnd == hwnd {
			p.hwnd = 0
		}
		p.mu.Unlock()
		closed()
	}()
	return <-up
}

// place sizes the window to the page (520 by height CSS pixels at the
// window's DPI), centres it on the work area, and shows it on top.
func place(hwnd uintptr, height int) {
	dpi, _, _ := procGetDpiForWindow.Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	r := windows.Rect{Right: int32(520 * int(dpi) / 96), Bottom: int32(height * int(dpi) / 96)}
	procAdjustWindowRectExForDpi.Call(uintptr(unsafe.Pointer(&r)), panelStyle, 0, 0, dpi)
	var area windows.Rect
	procSystemParametersInfo.Call(0x0030, 0, uintptr(unsafe.Pointer(&area)), 0) // SPI_GETWORKAREA
	w, h := r.Right-r.Left, r.Bottom-r.Top
	x, y := area.Left+(area.Right-area.Left-w)/2, area.Top+(area.Bottom-area.Top-h)/2
	procSetWindowPos.Call(hwnd, hwndTopmost, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0x0040|0x0020) // SWP_SHOWWINDOW | SWP_FRAMECHANGED
	procSetForegroundWindow.Call(hwnd)
}

func setTopmost(hwnd uintptr, on bool) {
	after := hwndNotopmost
	if on {
		after = hwndTopmost
	}
	procSetWindowPos.Call(hwnd, after, 0, 0, 0, 0, swpNoMoveNoSize)
}
