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

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
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
	Urgent      bool   `json:"urgent"`
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
// fails, or when an agent starts one (with its title, which a long
// prompt cuts short), the relay announces requests to connect, and the push service
// gets only ciphertext and a generic alert. Kinds the device did not ask
// for are not sent.
func TestPushNotices(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	fake := filepath.Join(binaries(t), "fakeacp")
	w := newOwnerMac(t, []string{"HERMESH_GEMINI_BIN=" + fake, "FAKE_ACP_STATE=" + filepath.Join(t.TempDir(), "fakeacp")},
		&localv1.GrantSpec{Agents: []string{"gemini"}}, "--retry-delay", "0") // its rate limit fails at once
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

	// A finished turn and a new session were not asked for: nothing is
	// sent for them.
	w.startSession(time.Minute, "--agent", "gemini", "--title", "写 hello", "--prompt", "say 好了", "--wait")
	time.Sleep(2 * time.Second)
	for _, s := range readSpool(t, spool, seen) {
		if n := open(s); n.Kind == "turn_done" || n.Kind == "session_started" {
			t.Fatalf("a %s notice nobody asked for: %+v", n.Kind, n)
		}
	}

	// A new session, once asked for: its title is the long prompt's start.
	register("question", "failure", "session_started")
	time.Sleep(time.Second)
	ns, r := w.startSession(time.Minute, "--agent", "gemini", "--prompt", "say "+strings.Repeat("长", 300))
	if ns.SessionID == "" {
		t.Fatalf("start: %+v", r)
	}
	if n := waitFor("session_started"); n.SessionId != ns.SessionID || n.Title != "say "+strings.Repeat("长", 36)+"…" ||
		n.Grant != "bot" || n.Machine != "owner-mac" || n.Project != "demo" || n.Agent != "gemini" {
		t.Fatalf("session_started notice: %+v", n)
	}
	for _, f := range mustGlob(spool) {
		if raw, _ := os.ReadFile(f); strings.Contains(string(raw), "长长") {
			t.Fatalf("plaintext in the push payload: %s", raw)
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

// TestPushHerNotices: Her on the owner Mac's own machine tells a device
// registered for "her" what the Mac app's notifications tell
// (notices.ts herNotices): her reply, a question she asks and an approval
// she waits for, one sealed notice each, which only the device opens. A
// muted assistant tells nothing, nor does unmuting it, nor her starting
// again with what she had.
func TestPushHerNotices(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	her, mockBin := herBinaries(t)
	mock := startHerMock(t, mockBin)
	w := newOwnerMac(t, herEnv(her, herConsole(t), herHome(t, mock.models), mock.base), &localv1.GrantSpec{})
	// Stopped as a service is, so that it stops her.
	t.Cleanup(func() { w.owner.stop() })
	spool := filepath.Join(t.TempDir(), "push")
	w.relayArgs = append(w.relayArgs, "--push-spool", spool)
	w.restartRelay()
	eventually(t, 30*time.Second, "the machine is back on the relay", func() bool {
		r := w.cli(30*time.Second, "project", "list")
		return r.code == 0 && strings.Contains(r.stdout, "owner-mac/demo")
	})
	c := w.app.client
	ctx := context.Background()
	mac := localMachine(t, c)
	turnHerOn(t, c, mac)
	bot := defaultBot(t, c, mac)

	key, _ := ecdh.X25519().GenerateKey(rand.Reader)
	var err error
	for i := 0; i < 50; i++ { // until the owner is back on the restarted relay
		if _, err = c.RegisterPush(ctx, &localv1.RegisterPushRequest{Token: "abc123", Environment: "development",
			NoticePublicKey: key.PublicKey().Bytes(), Kinds: []string{"her"}}); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	// The machine hears of the registration over its update stream.
	time.Sleep(time.Second)

	seen := map[string]bool{}
	notices := func() (out []*sessionv1.Notice) {
		t.Helper()
		for _, s := range readSpool(t, spool, seen) {
			// Sent at once, as the owner waits for her; but not time-sensitive, which would break through Focus.
			if s.Token != "abc123" || s.Payload.Hermesh.K != "her" || s.Payload.Aps.MutableContent != 1 || s.Payload.Aps.ThreadID == "" ||
				!s.Urgent || s.Payload.Aps.InterruptionLevel != "active" {
				t.Fatalf("payload: %+v", s)
			}
			sealed, _ := base64.StdEncoding.DecodeString(s.Payload.Hermesh.N)
			n := &sessionv1.Notice{}
			if err := identity.OpenNotice(key.Bytes(), sealed, n); err != nil {
				t.Fatalf("open notice: %v", err)
			}
			out = append(out, n)
		}
		return out
	}
	// one waits for the one notice a step makes: nothing more follows it.
	one := func(herKind, text string) {
		t.Helper()
		var got []*sessionv1.Notice
		eventually(t, 30*time.Second, "a "+herKind+" notice", func() bool {
			got = append(got, notices()...)
			return len(got) > 0
		})
		time.Sleep(2 * time.Second)
		got = append(got, notices()...)
		if n := got[0]; len(got) != 1 || n.Kind != "her" || n.HerKind != herKind || n.Text != text || n.MachineId != mac || n.Machine != "owner-mac" ||
			n.BotId != bot.ID || n.BotName != bot.Name || n.At == nil {
			t.Fatalf("want one %s notice %q, got %v", herKind, text, got)
		}
	}
	none := func(what string) {
		t.Helper()
		time.Sleep(2 * time.Second)
		if got := notices(); len(got) != 0 {
			t.Fatalf("%s: %v", what, got)
		}
	}
	send := func(text string) {
		t.Helper()
		if res, err := c.HerSend(ctx, &localv1.HerSendRequest{MachineId: mac, BotId: bot.ID, Text: text}); err != nil || !res.Delivered {
			t.Fatalf("HerSend: %v %v", res, err)
		}
	}
	mute := func(on bool) {
		t.Helper()
		res, err := c.HerCall(ctx, &localv1.HerCallRequest{MachineId: mac, Call: &herv1.CallRequest{Request: herSetup("bots/update", map[string]any{"bot_id": bot.ID, "muted": on})}})
		if err != nil || res.Response.GetError() != nil {
			t.Fatalf("bots/update: %v %v", res, err)
		}
	}

	// Her's reply.
	send(mockSend("早上好，今天有三件事。"))
	one("message", "早上好，今天有三件事。")

	// Muted, she tells nothing; unmuted, nothing either until she has more.
	mute(true)
	talk(t, c, mac, bot.ID, "静音时说的话")
	none("a muted assistant's notice")
	mute(false)
	none("a notice for unmuting")

	// A question.
	send("[[mock:ask:先发给谁？|财务|所有人]] 然后呢")
	one("question", "先发给谁？")

	// This Mac's hermeshd starting again, and her with it: what she had
	// before is no news.
	w.restartOwner(false)
	c = w.app.client
	eventually(t, 2*time.Minute, "her running again", func() bool { return listedMachine(t, c, mac).GetHer().GetRunning() })
	none("a notice for what her had before she started")

	// An approval: asking before each command, she waits for the owner.
	if _, err := c.SetHerAssistantMode(ctx, &localv1.SetHerAssistantModeRequest{MachineId: mac, BotId: bot.ID, PermissionMode: "manual"}); err != nil {
		t.Fatal(err)
	}
	send("[[mock:bash:touch 批准.txt]] 建个文件")
	one("approval", "touch 批准.txt")

	// The push service got none of it in the clear.
	for _, f := range mustGlob(spool) {
		raw, _ := os.ReadFile(f)
		for _, s := range []string{"早上好", "静音", "先发给谁", "批准", bot.ID, mac} {
			if strings.Contains(string(raw), s) {
				t.Fatalf("%q in the push payload: %s", s, raw)
			}
		}
	}
}

// TestPushNoticeWaitsForRelay: a session asks its question while the
// machine has no connection to the relay. The notice is not lost: it goes
// out once the machine is connected again. A question answered meanwhile
// (on the Mac itself, which needs no relay) is not announced any more.
func TestPushNoticeWaitsForRelay(t *testing.T) {
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
	eventually(t, 15*time.Second, "the device registered", func() bool {
		_, err := w.app.client.RegisterPush(ctx, &localv1.RegisterPushRequest{Token: "abc123", Environment: "development",
			NoticePublicKey: key.PublicKey().Bytes(), Kinds: []string{"question"}})
		return err == nil
	})
	time.Sleep(time.Second) // the machine hears of the registration over its update stream

	// Two sessions that ask after a while; the relay goes away before they do.
	kept, r := w.startSession(time.Minute, "--agent", "gemini", "--title", "留着问", "--prompt", "sleep 3s\nask 选哪个|红,蓝")
	answered, r2 := w.startSession(time.Minute, "--agent", "gemini", "--title", "已回答", "--prompt", "sleep 3s\nask 要吗|要,不要")
	if kept.SessionID == "" || answered.SessionID == "" {
		t.Fatalf("start: %+v %+v", r, r2)
	}
	w.relay.stop()
	asked := func(id string) *sessionv1.Question {
		res, _ := w.app.client.ListSessions(ctx, &localv1.ListSessionsRequest{})
		for _, s := range res.GetSessions() {
			if s.SessionId == id {
				return s.Progress.GetPendingQuestion()
			}
		}
		return nil
	}
	eventually(t, 30*time.Second, "both sessions ask", func() bool { return asked(kept.SessionID) != nil && asked(answered.SessionID) != nil })
	q := asked(answered.SessionID)
	if _, err := w.app.client.AnswerQuestion(ctx, &localv1.AnswerQuestionRequest{SessionId: answered.SessionID, QuestionId: q.QuestionId,
		Answers: []*sessionv1.Answer{{ItemId: q.Items[0].Id, Selected: []string{"要"}}}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if got := mustGlob(spool); len(got) != 0 {
		t.Fatalf("%d notices reached the push service with the relay stopped", len(got))
	}

	w.startRelay()
	seen := map[string]bool{}
	var got []*sessionv1.Notice
	read := func() {
		for _, s := range readSpool(t, spool, seen) {
			sealed, _ := base64.StdEncoding.DecodeString(s.Payload.Hermesh.N)
			n := &sessionv1.Notice{}
			if err := identity.OpenNotice(key.Bytes(), sealed, n); err != nil {
				t.Fatalf("open notice: %v", err)
			}
			got = append(got, n)
		}
	}
	eventually(t, 60*time.Second, "the question's notice, once the machine is connected again", func() bool {
		read()
		return len(got) > 0
	})
	time.Sleep(3 * time.Second) // what else would come has come
	read()
	if len(got) != 1 || got[0].Kind != "question" || got[0].SessionId != kept.SessionID || got[0].Title != "留着问" {
		t.Fatalf("notices after the relay came back: %+v", got)
	}
}
