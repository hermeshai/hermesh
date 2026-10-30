package e2e

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// TestMCPReachesAnIPhone: the owner's only trusted device is an iPhone,
// registered for push and not running, as a phone is most of the time
// (docs/onboarding.md §7.2). An MCP app that connects and uses Hermesh
// asks for approval at once, the push carrying it, and every page and
// note says to approve on the iPhone. Once that request lapses unanswered
// nothing asks again on a timer; when the phone connects, the request goes
// out again, and approved there, the same tokens work.
func TestMCPReachesAnIPhone(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	bin := binaries(t)
	tmp := t.TempDir()
	sockDir, _ := os.MkdirTemp(shortTemp, "hm")
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	ctx := context.Background()

	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	relayData := filepath.Join(tmp, "relay")
	spool := filepath.Join(tmp, "push")
	relay := start(t, "relay", nil, filepath.Join(bin, "hermesh-relay"), "serve", "--mcp", "--data-dir", relayData,
		"--listen", fmt.Sprintf("127.0.0.1:%d", port), "--push-spool", spool, "--pairing-ttl", "6s")
	eventually(t, 20*time.Second, "relay up", func() bool { return strings.Contains(relay.output(), "relay listening") })
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(bin, "hermesh-relay"), "claim-code", "--data-dir", relayData).stdout)

	// The iPhone: an owner role with no machine of its own, set up first.
	sock := filepath.Join(sockDir, "p.sock")
	phoneArgs := []string{"run", "--owner", "--data-dir", filepath.Join(tmp, "phone"), "--socket", sock}
	phone := start(t, "phone", nil, filepath.Join(bin, "hermeshd"), phoneArgs...)
	// What the phone's daemons said, when the test fails (it stops and starts one).
	var phones []*proc
	t.Cleanup(func() {
		for _, p := range phones {
			if t.Failed() {
				t.Logf("==== %s output ====\n%s", p.name, p.output())
			}
		}
	})
	phones = append(phones, phone)
	root, _ := identity.GenerateKey()
	app := newTestApp(t, sock, root)
	if _, err := app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: base, ClaimCode: claim, RootPublicKey: rootPub(root),
		RootLabel: "Alex's iPhone", RootPlatform: "ios"}); err != nil {
		t.Fatalf("setup: %v", err)
	}
	notice, _ := ecdh.X25519().GenerateKey(rand.Reader)
	eventually(t, 20*time.Second, "the push registration", func() bool {
		_, err := app.client.RegisterPush(ctx, &localv1.RegisterPushRequest{Token: "tok", Environment: "production",
			NoticePublicKey: notice.PublicKey().Bytes(), Kinds: []string{"pairing"}})
		return err == nil
	})
	address := mcpAddress(t, app)
	phone.stop() // in the background, as a phone is

	// The app connects: the pages say where to approve.
	mcpApp := newOAuthApp(t, address, "Hermesh test")
	page := mcpApp.authorize()
	if !strings.Contains(page, "approve it in Hermesh on your iPhone") || strings.Contains(page, "your Mac") {
		t.Fatalf("consent page for an owner with only an iPhone:\n%s", page)
	}
	progress := mcpApp.connect(page)
	if !strings.Contains(progress, "on your iPhone") {
		t.Fatalf("after connecting:\n%s", progress)
	}
	session := mcpApp.mcpSession(mcpApp.finish(progress).Access)

	// Using it sends the request now: the phone is offline but registered.
	// (A use while a request waits asks once more when it lapses, so the
	// test uses it only once.)
	doc := callTool(t, session, "project_list", nil)
	if !strings.Contains(doc.Note, "Hermesh on your iPhone") {
		t.Fatalf("the note while the request waits: %s", doc.raw)
	}
	seen := map[string]bool{}
	pushes := func() []*sessionv1.Notice {
		var out []*sessionv1.Notice
		for _, s := range readSpool(t, spool, seen) {
			sealed, _ := base64.StdEncoding.DecodeString(s.Payload.Hermesh.N)
			n := &sessionv1.Notice{}
			if err := identity.OpenNotice(notice.Bytes(), sealed, n); err != nil {
				t.Fatalf("open notice: %v", err)
			}
			out = append(out, n)
		}
		return out
	}
	eventually(t, 20*time.Second, "the request pushed to the phone", func() bool {
		for _, n := range pushes() {
			if n.Kind == "pairing" && strings.HasPrefix(n.PairingLabel, "Hermesh test") {
				return true
			}
		}
		return false
	})

	// It lapses unanswered. Nothing asks again on a timer.
	time.Sleep(12 * time.Second)
	if p := pushes(); len(p) != 0 {
		t.Fatalf("asked again with no use and no device: %d pushes", len(p))
	}

	// The phone comes back: the request is there again, and it approves it.
	phone = start(t, "phone again", nil, filepath.Join(bin, "hermeshd"), phoneArgs...)
	phones = append(phones, phone)
	app = newTestApp(t, sock, root)
	var code string
	eventually(t, 30*time.Second, "the request on the phone again", func() bool {
		l, err := app.client.ListPairingRequests(ctx, &localv1.ListPairingRequestsRequest{})
		if err != nil || len(l.Pairings) != 1 {
			return false
		}
		code = l.Pairings[0].Code
		return true
	})
	if _, err := app.client.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: code, Spec: &localv1.GrantSpec{}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "the same tokens working", func() bool { return statusOf(t, session).State == "active" })
	_ = phone
}
