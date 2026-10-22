package e2e

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// spooled is one notification the relay handed to the push service (its
// --push-spool file).
type spooled struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
	Payload     struct {
		Aps struct {
			Alert             map[string]string `json:"alert"`
			MutableContent    int               `json:"mutable-content"`
			InterruptionLevel string            `json:"interruption-level"`
			ThreadID          string            `json:"thread-id"`
		} `json:"aps"`
		Hermesh struct {
			N string `json:"n"`
			K string `json:"k"`
		} `json:"hermesh"`
	} `json:"payload"`
}

// readSpool returns the notifications in dir not seen yet, oldest first.
func readSpool(t *testing.T, dir string, seen map[string]bool) []spooled {
	t.Helper()
	names, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(names)
	var out []spooled
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		var s spooled
		b, _ := os.ReadFile(n)
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		out = append(out, s)
	}
	return out
}

// TestPushNotices: a trusted device registers for notifications; the
// machine seals a notice to its key when a session asks something or
// fails, the relay announces requests to connect, and the push service
// gets only ciphertext and a generic alert. Kinds the device did not ask
// for are not sent.
func TestPushNotices(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	fake := filepath.Join(binaries(t), "fakeacp")
	w := newOwnerMac(t, []string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp")},
		&localv1.GrantSpec{Agents: []string{"gemini"}})
	spool := filepath.Join(t.TempDir(), "push")
	w.relayArgs = append(w.relayArgs, "--push-spool", spool)
	w.restartRelay()
	eventually(t, 30*time.Second, "the machine is back on the relay", func() bool {
		r := w.cli(30*time.Second, "project", "list")
		return r.code == 0 && strings.Contains(r.stdout, "owner-mac/demo")
	})
	ctx := context.Background()

	key, _ := ecdh.X25519().GenerateKey(rand.Reader)
	register := func(kinds ...string) {
		t.Helper()
		var err error
		for i := 0; i < 50; i++ { // until the owner is back on the restarted relay
			if _, err = w.app.client.RegisterPush(ctx, &localv1.RegisterPushRequest{Token: "abc123", Environment: "development",
				NoticePublicKey: key.PublicKey().Bytes(), Kinds: kinds}); err == nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("register: %v", err)
	}
	register("question", "failure")
	// The machine hears of the registration over its update stream.
	time.Sleep(time.Second)

	seen := map[string]bool{}
	open := func(s spooled) *sessionv1.Notice {
		t.Helper()
		if s.Token != "abc123" || s.Environment != "development" || s.Payload.Aps.MutableContent != 1 || s.Payload.Aps.Alert["loc-key"] != "notice.body" {
			t.Fatalf("payload: %+v", s)
		}
		sealed, _ := base64.StdEncoding.DecodeString(s.Payload.Hermesh.N)
		n := &sessionv1.Notice{}
		if err := identity.OpenNotice(key.Bytes(), sealed, n); err != nil {
			t.Fatalf("open notice: %v", err)
		}
		return n
	}
	waitFor := func(kind string) *sessionv1.Notice {
		t.Helper()
		var got *sessionv1.Notice
		eventually(t, 30*time.Second, "a "+kind+" notice", func() bool {
			for _, s := range readSpool(t, spool, seen) {
				if n := open(s); n.Kind == kind {
					got = n
					return true
				}
			}
			return false
		})
		return got
	}

	// A question.
	q, r := w.startSession(time.Minute, "--agent", "gemini", "--title", "选颜色", "--prompt", "ask 选哪个颜色|红,蓝", "--wait")
	if q.SessionID == "" {
		t.Fatalf("start: %+v", r)
	}
	n := waitFor("question")
	if n.SessionId != q.SessionID || n.Title != "选颜色" || n.Grant != "bot" || n.Machine != "owner-mac" ||
		n.Question.GetItems()[0].GetText() != "选哪个颜色" || len(n.Question.Items[0].Options) != 2 {
		t.Fatalf("question notice: %+v", n)
	}
	// The spool and the payload carry no session content in the clear.
	for _, f := range mustGlob(spool) {
		raw, _ := os.ReadFile(f)
		if strings.Contains(string(raw), "选哪个颜色") || strings.Contains(string(raw), q.SessionID) {
			t.Fatalf("plaintext in the push payload: %s", raw)
		}
	}

	// A failure.
	w.startSession(time.Minute, "--agent", "gemini", "--title", "限流", "--prompt", "fail rate limited", "--wait")
	if n := waitFor("failure"); n.Title != "限流" || n.Failure == sessionv1.FailureKind_FAILURE_KIND_UNSPECIFIED {
		t.Fatalf("failure notice: %+v", n)
	}

	// A finished turn was not asked for: nothing is sent for it.
	w.startSession(time.Minute, "--agent", "gemini", "--title", "写 hello", "--prompt", "say 好了", "--wait")
	time.Sleep(2 * time.Second)
	for _, s := range readSpool(t, spool, seen) {
		if n := open(s); n.Kind == "turn_done" {
			t.Fatalf("a turn_done notice nobody asked for: %+v", n)
		}
	}

	// A request to connect, from the relay itself, with the code to compare.
	login := start(t, "login", []string{"HERMESH_HOME=" + filepath.Join(t.TempDir(), "agent2")}, filepath.Join(w.bin, "hermesh"), "auth", "login", w.addr, "--name", "Cursor")
	code := login.pairingCode(t)
	p := waitFor("pairing")
	if p.PairingKind != identityv1.PairingKind_PAIRING_KIND_CLIENT || p.PairingLabel != "Cursor" || p.PairingCode != code || p.VerificationCode == "" {
		t.Fatalf("pairing notice: %+v", p)
	}

	// Stopping: an empty token. Nothing more is sent (once what was under
	// way is delivered).
	time.Sleep(time.Second)
	readSpool(t, spool, seen)
	if _, err := w.app.client.RegisterPush(ctx, &localv1.RegisterPushRequest{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	w.startSession(time.Minute, "--agent", "gemini", "--title", "又问", "--prompt", "ask 还要吗|要,不要", "--wait")
	time.Sleep(2 * time.Second)
	if got := readSpool(t, spool, seen); len(got) != 0 {
		t.Fatalf("notices after unregistering: %d", len(got))
	}
}

func mustGlob(dir string) []string {
	m, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	return m
}
