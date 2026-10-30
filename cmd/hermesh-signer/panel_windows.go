package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/hermeshai/hermesh/internal/preview"
	"github.com/hermeshai/hermesh/internal/signer"
)

// panel asks the owner in a window of the signer's own: what a batch
// signs (a task dialog, at the signer's high integrity, so programs of
// theirs that are not elevated can neither change nor click it), then
// Windows Hello, while the dialog stays open.

var (
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	procSetWindowPos                  = user32.NewProc("SetWindowPos")
	procMessageBox                    = user32.NewProc("MessageBoxW")
	kernel32                          = windows.NewLazySystemDLL("kernel32.dll")
	procGetUserDefaultUILanguage      = kernel32.NewProc("GetUserDefaultUILanguage")
	procCreateActCtx                  = kernel32.NewProc("CreateActCtxW")
	procActivateActCtx                = kernel32.NewProc("ActivateActCtx")
)

const (
	approveID = 1001
	denyID    = 1002
	// Task dialog flags and notifications.
	tdfAllowCancel     = 0x0008
	tdfSizeToContent   = 0x01000000
	tdnCreated         = 0
	tdnButtonClicked   = 2
	tdnDestroyed       = 5
	shieldIcon         = 0xFFFC // MAKEINTRESOURCE(-4): TD_SHIELD_ICON
	warningIcon        = 0xFFFF // TD_WARNING_ICON
	errorIcon          = 0xFFFE // TD_ERROR_ICON
	swpNoMoveNoSize    = 0x0001 | 0x0002
	hwndTopmost        = ^uintptr(0) // (HWND)-1
	hwndNotopmost      = ^uintptr(1) // (HWND)-2
	sFalse             = 1
	mbYesNoIconWarning = 0x04 | 0x30 | 0x100 | 0x40000 // MB_YESNO | MB_ICONWARNING | MB_DEFBUTTON2 | MB_TOPMOST
	idYes              = 6
)

type panel struct {
	w words

	mu   sync.Mutex
	hwnd windows.HWND // the dialog open now

	// While a dialog is open: the outcome of Windows Hello after Approve.
	approved bool
	helloErr error
}

func newPanel() *panel {
	procSetProcessDpiAwarenessContext.Call(^uintptr(3)) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
	lang := "en"
	if id, _, _ := procGetUserDefaultUILanguage.Call(); id&0x3ff == 0x04 { // LANG_CHINESE
		lang = "zh-CN"
	}
	return &panel{w: loadWords(lang)}
}

// Approve shows the cards and, after Approve, asks Windows Hello.
func (p *panel) Approve(_ context.Context, cards []preview.Card) (ok bool, err error) {
	onOwnThread(func() { ok, err = p.approve(cards) })
	return ok, err
}

// onOwnThread runs f on an OS thread that ends with it: a window lives on
// its thread, and what f leaves in the thread's COM (Windows Hello makes
// it multithreaded) never reaches other code, a WebView2 panel's above all.
func onOwnThread(f func()) {
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread() // never unlocked
		defer close(done)
		f()
	}()
	<-done
}

func (p *panel) approve(cards []preview.Card) (bool, error) {
	instruction := fmt.Sprintf(p.w.Several, len(cards))
	var content, warnings []string
	for _, c := range cards {
		if len(cards) == 1 {
			instruction = c.Title
		} else {
			content = append(content, c.Title)
		}
		if c.Code != "" {
			content = append(content, fmt.Sprintf(p.w.Code, c.Code))
		}
		content = append(content, c.Lines...)
		if c.Why != "" {
			content = append(content, c.Why)
		}
		if c.Warning != "" {
			warnings = append(warnings, c.Warning)
		}
		content = append(content, "")
	}
	button, err := p.dialog(instruction, strings.TrimSpace(strings.Join(content, "\n")), strings.Join(warnings, "\n"), true)
	if err != nil {
		return false, err
	}
	if button != approveID || !p.approved {
		if errors.Is(p.helloErr, errNoHello) {
			return false, errNoHello
		}
		return false, p.helloErr
	}
	return true, nil
}

// Failed tells why a batch was not signed, unless the owner declined it.
func (p *panel) Failed(err error) {
	if errors.Is(err, signer.ErrRefused) {
		return
	}
	if errors.Is(err, errNoHello) {
		err = errors.New(p.w.NoHello)
	}
	fmt.Fprintln(os.Stderr, "hermesh-signer: not signed:", err)
	onOwnThread(func() { p.dialog(p.w.NotSigned, err.Error(), "", false) })
}

// Show brings the open dialog forward.
func (p *panel) Show() {
	p.mu.Lock()
	h := p.hwnd
	p.mu.Unlock()
	if h != 0 {
		procSetForegroundWindow.Call(uintptr(h))
	}
}

var (
	callbackOnce sync.Once
	callback     uintptr
	// The panel whose dialog is open (one at a time).
	current *panel
)

// dialog shows a task dialog: Approve and Deny (Deny the default), or
// only OK. It reports the button chosen.
func (p *panel) dialog(instruction, content, footer string, ask bool) (int, error) {
	if !commonControls() {
		return p.messageBox(instruction, content, footer, ask)
	}
	callbackOnce.Do(func() { callback = syscall.NewCallback(dialogEvent) })
	p.approved, p.helloErr = false, signer.ErrRefused
	current = p
	str := func(s string) uintptr {
		if s == "" {
			return 0
		}
		u, _ := windows.UTF16PtrFromString(s)
		return uintptr(unsafe.Pointer(u))
	}
	var keep []uintptr // the strings, alive until the dialog ends
	text := func(s string) uintptr { v := str(s); keep = append(keep, v); return v }
	// TASKDIALOG_BUTTONs and TASKDIALOGCONFIG are packed (pshpack1).
	buttons := make([]byte, 2*12)
	nButtons := 0
	if ask {
		for i, b := range []struct {
			id   int
			text string
		}{{approveID, p.w.Approve}, {denyID, p.w.Deny}} {
			binary.LittleEndian.PutUint32(buttons[12*i:], uint32(b.id))
			binary.LittleEndian.PutUint64(buttons[12*i+4:], uint64(text(b.text)))
		}
		nButtons = 2
	}
	cfg := make([]byte, 160)
	put32 := func(off int, v uint32) { binary.LittleEndian.PutUint32(cfg[off:], v) }
	put64 := func(off int, v uintptr) { binary.LittleEndian.PutUint64(cfg[off:], uint64(v)) }
	put32(0, uint32(len(cfg)))
	put32(20, tdfAllowCancel|tdfSizeToContent)
	if !ask {
		put32(24, 0x0001) // TDCBF_OK_BUTTON
	}
	put64(28, text(p.w.Title))
	icon := uintptr(shieldIcon)
	if !ask {
		icon = errorIcon
	}
	put64(36, icon)
	put64(44, text(instruction))
	put64(52, text(content))
	put32(60, uint32(nButtons))
	if nButtons > 0 {
		put64(64, uintptr(unsafe.Pointer(&buttons[0])))
		put32(72, denyID)
	}
	if footer != "" {
		put64(124, warningIcon)
		put64(132, text(footer))
	}
	put64(140, callback)
	var button int32
	r, _, _ := procTaskDialogIndirect.Call(uintptr(unsafe.Pointer(&cfg[0])), uintptr(unsafe.Pointer(&button)), 0, 0)
	runtime.KeepAlive(keep)
	runtime.KeepAlive(buttons)
	if err := hresult("TaskDialogIndirect", r); err != nil {
		return 0, err
	}
	return int(button), nil
}

// dialogEvent is the task dialog's callback: it keeps the dialog on top,
// and runs Windows Hello when Approve is clicked, the dialog still open.
func dialogEvent(hwnd, msg, wparam, lparam, data uintptr) uintptr {
	p := current
	switch msg {
	case tdnCreated:
		p.mu.Lock()
		p.hwnd = windows.HWND(hwnd)
		p.mu.Unlock()
		procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMoveNoSize)
		procSetForegroundWindow.Call(hwnd)
	case tdnButtonClicked:
		if wparam == approveID {
			// Off the top while Windows Hello asks, whose window would be under it.
			procSetWindowPos.Call(hwnd, hwndNotopmost, 0, 0, 0, 0, swpNoMoveNoSize)
			p.approved, p.helloErr = hello(windows.HWND(hwnd), p.w.Hello)
			if !p.approved && p.helloErr == nil {
				procSetWindowPos.Call(hwnd, hwndTopmost, 0, 0, 0, 0, swpNoMoveNoSize)
				return sFalse // not verified: the dialog stays, to approve again or deny
			}
		}
	case tdnDestroyed:
		p.mu.Lock()
		p.hwnd = 0
		p.mu.Unlock()
	}
	return 0
}

var (
	actCtxOnce             sync.Once
	actCtxOK               bool
	procTaskDialogIndirect *windows.LazyProc
)

// commonControls loads version 6 of the common controls, which has task
// dialogs, through the manifest setup put beside the program (Program
// Files, where only administrators write); without one, plain message
// boxes stand in.
func commonControls() bool {
	actCtxOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		src, _ := windows.UTF16PtrFromString(exe + ".manifest")
		// ACTCTXW: cbSize, dwFlags, lpSource, then fields unused here.
		var ctx [56]byte
		binary.LittleEndian.PutUint32(ctx[0:], uint32(len(ctx)))
		binary.LittleEndian.PutUint64(ctx[8:], uint64(uintptr(unsafe.Pointer(src))))
		h, _, _ := procCreateActCtx.Call(uintptr(unsafe.Pointer(&ctx[0])))
		if windows.Handle(h) == windows.InvalidHandle {
			return
		}
		var cookie uintptr
		if r, _, _ := procActivateActCtx.Call(h, uintptr(unsafe.Pointer(&cookie))); r == 0 {
			return
		}
		procTaskDialogIndirect = windows.NewLazySystemDLL("comctl32.dll").NewProc("TaskDialogIndirect")
		actCtxOK = procTaskDialogIndirect.Find() == nil
	})
	return actCtxOK
}

// messageBox stands in for the task dialog: Yes approves (then Windows
// Hello), No denies.
func (p *panel) messageBox(instruction, content, footer string, ask bool) (int, error) {
	text := strings.TrimSpace(instruction + "\n\n" + content + "\n\n" + footer)
	if ask {
		text += "\n\n" + p.w.Approve + "?"
	}
	t, _ := windows.UTF16PtrFromString(text)
	title, _ := windows.UTF16PtrFromString(p.w.Title)
	flags := uintptr(mbYesNoIconWarning)
	if !ask {
		flags = 0x10 | 0x40000 // MB_ICONERROR | MB_TOPMOST
	}
	r, _, _ := procMessageBox.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(title)), flags)
	if !ask || r != idYes {
		return denyID, nil
	}
	p.approved, p.helloErr = hello(0, p.w.Hello)
	return approveID, nil
}
