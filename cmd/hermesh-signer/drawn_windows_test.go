package main

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/hermeshai/hermesh/internal/preview"
	"github.com/hermeshai/hermesh/internal/signer"
)

// The batches the panel shows, in Chinese as on the owner's PC.
var (
	joinBatch = []preview.Card{
		{Title: "让这台电脑加入你的 Hermesh", Code: "XY7Q-N365-786M", Own: true,
			Lines: []string{"设备名称：YIPENGFEI38BE", "账号：o_zwbpwtikcuvdpon2irjfzm6e"},
			Why:   "签下这台电脑的加入申请。之后到你已有的设备上核对同样的核对码并批准，这台电脑就成为可信设备。"},
		{Title: "给这台电脑签发端到端连接证书", Own: true, Lines: []string{"设备名称：YIPENGFEI38BE", "指纹：tzh3 fjxs"}, Why: "这台电脑上的 Hermesh 用它和你的机器建立端到端加密连接。"},
		{Title: "共享这台电脑上的 agent", Own: true, Lines: []string{"机器：yipengfei38be"}, Why: "Hermesh 像你一样在命令行调用它们，不读取 API Key 和 Token。"},
	}
	grantBatch = []preview.Card{{Title: "允许“bot”连接你的电脑", Code: "K4TP-9WQ2-HM3V",
		Lines:   []string{"项目：全部", "机器：vps-1", "编码 Agent：claude、codex", "到期：2026年11月29日", "指纹：ab12 cd34"},
		Why:     "用你的账号密钥给它签发授权，随时可以撤销。",
		Warning: "完全信任：在选定的项目里，它能做你能做的任何事。"}}
	severalBatch = []preview.Card{
		{Title: "让“vps-2”成为你的机器", Code: "M2QX-7HTA-01PZ", Lines: []string{"机器：vps-2", "指纹：9f3a 77c1"}},
		{Title: "允许“grok”连接你的电脑", Code: "K4TP-9WQ2-HM3V", Lines: []string{"项目：demo", "到期：2026年11月29日"}, Warning: "完全信任：在选定的项目里，它能做你能做的任何事。"},
		{Title: "撤销“old-bot”的授权", Lines: []string{"授权：c_7h2kq"}},
	}
)

func testPanel(dark bool, hello func() (bool, error)) *drawnPanel {
	p := newDrawnPanel()
	p.w = loadWords("zh-CN")
	p.dark = func() bool { return dark }
	if hello != nil {
		p.hello = func(windows.HWND, string) (bool, error) { return hello() }
	}
	return p
}

type answer struct {
	ok  bool
	err error
}

// ask opens the panel for cards and waits until it shows.
func ask(t *testing.T, ctx context.Context, p *drawnPanel, cards []preview.Card) (*view, chan answer) {
	t.Helper()
	ch := make(chan answer, 1)
	go func() {
		ok, err := p.Approve(ctx, cards)
		ch <- answer{ok, err}
	}()
	return shown(t, p), ch
}

func shown(t *testing.T, p *drawnPanel) *view {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		p.mu.Lock()
		v := p.open
		p.mu.Unlock()
		if v != nil {
			if r, _, _ := procIsWindowVisible.Call(v.window()); r != 0 {
				time.Sleep(200 * time.Millisecond)
				return v
			}
		}
	}
	t.Fatal("the panel did not show")
	return nil
}

func answered(t *testing.T, ch chan answer) answer {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(10 * time.Second):
		t.Fatal("the panel did not answer")
		return answer{}
	}
}

func post(v *view, msg, w, l uintptr) { procPostMessage.Call(v.window(), msg, w, l) }

func key(v *view, vk uintptr) { post(v, wmKeyDown, vk, 0) }

func click(v *view, id string) {
	r := v.targets[id]
	x, y := r.x+r.w/2, r.y+r.h/2
	if v.inMain[id] {
		y -= v.scroll
	}
	l := uintptr(y)<<16 | uintptr(x)
	post(v, wmLButtonDown, 1, l)
	post(v, wmLButtonUp, 0, l)
	time.Sleep(200 * time.Millisecond)
}

// shot saves what the panel shows as a PNG in $HERMESH_PANEL_SHOTS, to
// look at against the design.
func shot(t *testing.T, v *view, name string) {
	t.Helper()
	dir := os.Getenv("HERMESH_PANEL_SHOTS")
	if dir == "" {
		return
	}
	w, h := v.w, v.viewH+v.footH
	screen, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, screen)
	dc, _, _ := procCreateCompatibleDC.Call(screen)
	defer procDeleteDC.Call(dc)
	bmp, _, _ := procCreateCompatibleBitmap.Call(screen, uintptr(w), uintptr(h))
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelectObject.Call(dc, bmp)
	v.paint(dc)
	procSelectObject.Call(dc, old)
	info := struct {
		size                     uint32
		width, height            int32
		planes, bits             uint16
		compression, image       uint32
		xppm, yppm, used, needed int32
	}{width: int32(w), height: -int32(h), planes: 1, bits: 32}
	info.size = uint32(unsafe.Sizeof(info))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	procGetDIBits.Call(dc, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&img.Pix[0])), uintptr(unsafe.Pointer(&info)), 0)
	for i := 0; i < len(img.Pix); i += 4 { // BGRA to RGBA, opaque
		img.Pix[i], img.Pix[i+2], img.Pix[i+3] = img.Pix[i+2], img.Pix[i], 255
	}
	os.MkdirAll(dir, 0o755)
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, img)
}

// TestDrawnPanelShows each kind of batch, light and dark, and Escape
// denies it.
func TestDrawnPanelShows(t *testing.T) {
	for _, c := range []struct {
		name  string
		cards []preview.Card
		dark  bool
	}{{"join", joinBatch, false}, {"join-dark", joinBatch, true}, {"grant", grantBatch, false}, {"several", severalBatch, false}, {"several-dark", severalBatch, true}} {
		t.Run(c.name, func(t *testing.T) {
			p := testPanel(c.dark, nil)
			v, ch := ask(t, context.Background(), p, c.cards)
			shot(t, v, c.name)
			if c.name == "join" {
				click(v, "details")
				shot(t, v, "join-details")
			}
			key(v, 0x1B) // Escape
			if a := answered(t, ch); a.ok || !errors.Is(a.err, signer.ErrRefused) {
				t.Fatalf("Escape: %+v", a)
			}
		})
	}
}

// TestDrawnPanelAnswers: Enter on the default denies; Approve asks
// Windows Hello, waits while it asks, and stays to approve again when it
// was not verified; verified, the panel approves; an error of Windows
// Hello's is the answer; a request that went away closes the panel.
func TestDrawnPanelAnswers(t *testing.T) {
	v, ch := ask(t, context.Background(), testPanel(false, nil), grantBatch)
	key(v, 0x0D) // Enter
	if a := answered(t, ch); a.ok || !errors.Is(a.err, signer.ErrRefused) {
		t.Fatalf("Enter: %+v", a)
	}

	var asked atomic.Int32
	release := make(chan bool)
	p := testPanel(false, func() (bool, error) {
		asked.Add(1)
		return <-release, nil
	})
	v, ch = ask(t, context.Background(), p, joinBatch)
	click(v, "approve")
	shot(t, v, "join-waiting")
	key(v, 0x1B) // Escape while Windows Hello asks: nothing
	release <- false
	time.Sleep(300 * time.Millisecond)
	select {
	case a := <-ch:
		t.Fatalf("answered after Windows Hello was cancelled: %+v", a)
	default:
	}
	click(v, "approve")
	release <- true
	if a := answered(t, ch); !a.ok || a.err != nil || asked.Load() != 2 {
		t.Fatalf("approved: %+v after %d asks", a, asked.Load())
	}

	v, ch = ask(t, context.Background(), testPanel(false, func() (bool, error) { return false, errNoHello }), grantBatch)
	click(v, "approve")
	if a := answered(t, ch); a.ok || !errors.Is(a.err, errNoHello) {
		t.Fatalf("no Windows Hello: %+v", a)
	}

	ctx, cancel := context.WithCancel(context.Background())
	_, ch = ask(t, ctx, testPanel(false, nil), grantBatch)
	cancel()
	if a := answered(t, ch); a.ok || a.err == nil {
		t.Fatalf("withdrawn: %+v", a)
	}
}

// TestDrawnPanelFailed tells why nothing was signed, and OK closes it.
func TestDrawnPanelFailed(t *testing.T) {
	for _, dark := range []bool{false, true} {
		p := testPanel(dark, nil)
		p.Failed(errNoHello)
		v := shown(t, p)
		name := "failed"
		if dark {
			name += "-dark"
		}
		shot(t, v, name)
		click(v, "ok")
		for end := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
			p.mu.Lock()
			open := p.open
			p.mu.Unlock()
			if open == nil {
				break
			}
			if time.Now().After(end) {
				t.Fatal("OK did not close it")
			}
		}
	}
}
