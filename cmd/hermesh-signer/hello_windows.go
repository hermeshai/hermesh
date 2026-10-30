package main

import (
	"errors"
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows Hello, as UserConsentVerifier asks it for a window of ours
// (IUserConsentVerifierInterop): the face, the fingerprint or the PIN of
// the person signed in. Through WinRT's ABI, without cgo: COM objects are
// pointers to tables of methods.

var (
	combase                    = windows.NewLazySystemDLL("combase.dll")
	procRoInitialize           = combase.NewProc("RoInitialize")
	procRoGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	procWindowsCreateString    = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString    = combase.NewProc("WindowsDeleteString")
	user32                     = windows.NewLazySystemDLL("user32.dll")
	procPeekMessage            = user32.NewProc("PeekMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessage        = user32.NewProc("DispatchMessageW")

	// IUserConsentVerifierInterop and IAsyncInfo.
	iidVerifierInterop = guid{0x39E050C3, 0x4E74, 0x441A, [8]byte{0x8D, 0xC0, 0xB8, 0x11, 0x04, 0xDF, 0x94, 0x9C}}
	iidAsyncInfo       = guid{0x00000036, 0x0000, 0x0000, [8]byte{0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}}
	// IAsyncOperation<UserConsentVerificationResult>.
	iidVerification = winrtIID(asyncOperation("enum(Windows.Security.Credentials.UI.UserConsentVerificationResult;i4)"))

	errNoHello = errors.New("Windows Hello is not set up")
)

// UserConsentVerificationResult values.
const (
	verified             = 0
	deviceNotPresent     = 1
	notConfiguredForUser = 2
	disabledByPolicy     = 3
)

// object is a COM object: a pointer to its table of methods.
type object struct{ vtbl *[16]uintptr }

// call calls its n-th method.
func (o *object) call(n int, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(o.vtbl[n], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return r
}

func (o *object) release() {
	if o != nil {
		o.call(2) // IUnknown::Release
	}
}

func hresult(call string, r uintptr) error {
	if int32(r) < 0 {
		return fmt.Errorf("%s: %#x", call, uint32(r))
	}
	return nil
}

func hstring(s string) (uintptr, error) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h uintptr
	r, _, _ := procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h)))
	return h, hresult("WindowsCreateString", r)
}

// hello asks Windows Hello for the window owner, saying message, and waits
// for the person, keeping owner's thread's windows answering meanwhile.
// It reports whether they were verified; errNoHello when Windows Hello is
// not set up or not allowed here.
func hello(owner windows.HWND, message string) (bool, error) {
	procRoInitialize.Call(0) // single-threaded, a window's thread; an earlier call is fine
	class, err := hstring("Windows.Security.Credentials.UI.UserConsentVerifier")
	if err != nil {
		return false, err
	}
	defer procWindowsDeleteString.Call(class)
	var factory *object
	r, _, _ := procRoGetActivationFactory.Call(class, uintptr(unsafe.Pointer(&iidVerifierInterop)), uintptr(unsafe.Pointer(&factory)))
	if err := hresult("RoGetActivationFactory", r); err != nil {
		return false, err
	}
	defer factory.release()
	msg, err := hstring(message)
	if err != nil {
		return false, err
	}
	defer procWindowsDeleteString.Call(msg)
	// IInspectable's six methods, then RequestVerificationForWindowAsync.
	var op *object
	if err := hresult("RequestVerificationForWindowAsync", factory.call(6, uintptr(owner), msg, uintptr(unsafe.Pointer(&iidVerification)), uintptr(unsafe.Pointer(&op)))); err != nil {
		return false, err
	}
	defer op.release()
	var info *object
	if err := hresult("QueryInterface(IAsyncInfo)", op.call(0, uintptr(unsafe.Pointer(&iidAsyncInfo)), uintptr(unsafe.Pointer(&info)))); err != nil {
		return false, err
	}
	defer info.release()
	// IAsyncInfo::get_Status: 0 while it runs.
	for {
		var st int32
		if err := hresult("IAsyncInfo.Status", info.call(7, uintptr(unsafe.Pointer(&st)))); err != nil {
			return false, err
		}
		if st != 0 {
			break
		}
		pump()
		time.Sleep(30 * time.Millisecond)
	}
	var result int32
	if err := hresult("GetResults", op.call(8, uintptr(unsafe.Pointer(&result)))); err != nil {
		return false, err
	}
	switch result {
	case verified:
		return true, nil
	case deviceNotPresent, notConfiguredForUser, disabledByPolicy:
		return false, errNoHello
	}
	return false, nil // canceled, retries exhausted, busy
}

// pump answers this thread's window messages that are waiting.
func pump() {
	var m [48]byte // MSG
	for {
		if r, _, _ := procPeekMessage.Call(uintptr(unsafe.Pointer(&m[0])), 0, 0, 0, 1 /* PM_REMOVE */); r == 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m[0])))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m[0])))
	}
}
