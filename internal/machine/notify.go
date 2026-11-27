package machine

import (
	"bytes"
	"cmp"
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	rt "github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// Notices for the owner's iPhones. When a session asks something, fails or
// finishes a turn, the machine seals a notice to the notice key of each
// trusted device that asked for that kind (its push registration, signed by
// its device key, which a root key certifies) and hands the ciphertext to
// the relay for the push service. The relay never sees what it says. A
// new session is announced too, and so is what Her has for the owner while
// she runs here.

// pushTarget is a verified push registration.
type pushTarget struct {
	keyID string
	key   []byte
	kinds map[string]bool
}

// notices keeps the push targets and the notices the relay has not taken.
type notices struct {
	mu      sync.Mutex
	targets []pushTarget
	// waiting is, per thread, the newest notice the relay has not taken:
	// the machine was not connected, or the call failed. It goes out once
	// the machine is connected again (sendWaitingNotices).
	waiting map[string]waitingNotice
}

// waitingNotice is a notice sealed for each device, kept for the relay.
type waitingNotice struct {
	at     time.Time
	kind   string
	sealed []*relayv1.SealedNotice
}

const (
	// noticeKept: a notice older than this is not sent any more (the push
	// service keeps one an hour too).
	noticeKept = time.Hour
	// noticesKept: at most this many threads wait; the oldest goes first.
	noticesKept = 64
)

func (n *Machine) acceptPushTargets(pts *relayv1.PushTargets) {
	n.mu.Lock()
	trust, owner := n.trust, n.ownerID
	n.mu.Unlock()
	if trust == nil {
		return
	}
	var ts []pushTarget
	for _, pt := range pts.GetTargets() {
		dc, _, err := trust.VerifyDeviceCert(pt.DeviceCert)
		if err != nil {
			continue
		}
		reg := &identityv1.PushRegistration{}
		pub, err := identity.SelfSigned(pt.Registration, reg, func(r *identityv1.PushRegistration) []byte { return r.DevicePublicKey })
		if err != nil || reg.OwnerId != owner || !bytes.Equal(reg.DevicePublicKey, dc.DevicePublicKey) || len(reg.NoticePublicKey) != 32 {
			continue
		}
		kinds := map[string]bool{}
		for _, k := range reg.Kinds {
			kinds[k] = true
		}
		ts = append(ts, pushTarget{keyID: identity.KeyID(pub), key: reg.NoticePublicKey, kinds: kinds})
	}
	n.notices.mu.Lock()
	n.notices.targets = ts
	n.notices.mu.Unlock()
}

// sessionNotice tells the owner's devices of event ev, if it is one they asked for.
func (n *Machine) sessionNotice(sess *sessionv1.Session, ev *sessionv1.SessionEvent) {
	if sess == nil || ev == nil {
		return
	}
	agent := cmp.Or(sess.Agent, sess.GetModelSelection().GetInstanceId())
	createdBy := cmp.Or(sess.CreatedBy, sess.GetActor().GetId())
	nt := &sessionv1.Notice{
		At:        timestamppb.Now(),
		SessionId: sess.SessionId,
		Title:     sess.Title,
		Project:   sess.ProjectId,
		Agent:     agent,
	}
	switch ev.Type {
	case "session.created":
		if sess.Status != sessionv1.SessionStatus_SESSION_STATUS_QUEUED &&
			sess.Status != sessionv1.SessionStatus_SESSION_STATUS_PREPARING &&
			sess.Status != sessionv1.SessionStatus_SESSION_STATUS_RUNNING {
			return
		}
		nt.Kind = "session_started"
	case "request.created":
		switch ev.GetRequest().GetKind() {
		case sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT:
			q := sess.GetProgress().GetPendingQuestion()
			if q == nil {
				return
			}
			nt.Kind = "question"
			nt.Turn = q.GetTurn()
			nt.Question = trimQuestion(q)
		case sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_TOOL_APPROVAL,
			sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_PLAN_REVIEW,
			sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_SECRET_INPUT:
			nt.Kind = "session_request"
		default:
			return
		}
	case "request.resolved":
		n.dropWaitingNotice(sess.SessionId, "question")
		n.dropWaitingNotice(sess.SessionId, "session_request")
		return
	case "run.completed":
		if sess.Status == sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT {
			return
		}
		nt.Text, _ = rt.Truncate(sess.Result, 720)
		nt.Kind = "turn_done"
		nt.Turn = cmp.Or(ev.GetRun().GetOrdinal(), sess.GetProgress().GetTurnCount())
		nt.DiffStat = sess.GetProgress().GetDiffStat()
	case "run.failed":
		f := sess.GetProgress().GetLastFailure()
		if f != nil && f.Kind == sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED {
			return
		}
		nt.Kind = "failure"
		nt.Turn = cmp.Or(ev.GetRun().GetOrdinal(), sess.GetProgress().GetTurnCount())
		nt.Failure = f.GetKind()
		nt.FailureMessage, _ = rt.Truncate(cmp.Or(f.GetMessage(), sess.GetError()), 300)
	case "session.recovered":
		if sess.Status != sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
			return
		}
		f := sess.GetProgress().GetLastFailure()
		nt.Kind = "failure"
		nt.Turn = sess.GetProgress().GetTurnCount()
		nt.Failure = f.GetKind()
		nt.FailureMessage, _ = rt.Truncate(cmp.Or(f.GetMessage(), sess.GetError()), 300)
	default:
		return
	}
	n.notify(nt, sess.SessionId, func() {
		nt.Grant = n.knownClients()[createdBy].Label
		nt.Machine = n.Name()
	})
}

// herNotice tells the owner's devices of what Her's newest became
// (herhost.Config.Notice): her reply, a question, or what waits for the
// owner.
func (n *Machine) herNotice(l *machinev1.HerLatest) {
	nt := &sessionv1.Notice{Kind: "her", At: l.At, HerKind: "message", MachineId: n.MachineID(), BotId: l.BotId, BotName: l.BotName, Text: l.Text}
	switch l.Kind {
	case machinev1.HerLatestKind_HER_LATEST_KIND_QUESTION:
		nt.HerKind = "question"
	case machinev1.HerLatestKind_HER_LATEST_KIND_APPROVAL:
		nt.HerKind = "approval"
	}
	n.notify(nt, "her:"+l.BotId, func() { nt.Machine = n.Name() })
}

// notify seals nt to the notice key of each device that asked for its
// kind, completed by fill once one did, and hands the ciphertexts to the
// relay.
func (n *Machine) notify(nt *sessionv1.Notice, thread string, fill func()) {
	n.notices.mu.Lock()
	targets := n.notices.targets
	n.notices.mu.Unlock()
	var out []*relayv1.SealedNotice
	subscription := nt.Kind
	if nt.Kind == "her" {
		subscription = "her_reply"
		if nt.HerKind == "question" || nt.HerKind == "approval" {
			subscription = "her_request"
		}
	}
	for _, t := range targets {
		// "her" is the original subscription for all Her notifications.
		if !t.kinds[nt.Kind] && !t.kinds[subscription] {
			continue
		}
		if len(out) == 0 {
			fill()
		}
		sealed, err := identity.SealNotice(t.key, nt)
		if err != nil {
			continue
		}
		out = append(out, &relayv1.SealedNotice{DeviceKeyId: t.keyID, Sealed: sealed, Kind: nt.Kind, Thread: thread})
	}
	if len(out) == 0 {
		return
	}
	w := waitingNotice{at: time.Now(), kind: nt.Kind, sealed: out}
	n.notices.mu.Lock()
	delete(n.notices.waiting, thread) // this one is newer
	n.notices.mu.Unlock()
	n.mu.Lock()
	conn := n.conn
	n.mu.Unlock()
	if conn == nil {
		n.keepNotice(thread, w)
		return
	}
	go func() {
		if err := n.handNotice(conn, w); err != nil {
			n.log.Debug("notice kept for later", "kind", w.kind, "thread", thread, "err", err)
			n.keepNotice(thread, w)
		}
	}()
}

// handNotice hands a sealed notice to the relay.
func (n *Machine) handNotice(conn *transport.RelayConn, w waitingNotice) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := n.control(conn)
	if err != nil {
		return err
	}
	_, err = c.Notify(ctx, &relayv1.NotifyRequest{Notices: w.sealed})
	return err
}

// keepNotice keeps w for when the machine is connected again, unless a
// newer one of its thread waits already.
func (n *Machine) keepNotice(thread string, w waitingNotice) {
	n.notices.mu.Lock()
	defer n.notices.mu.Unlock()
	if cur, ok := n.notices.waiting[thread]; ok && cur.at.After(w.at) {
		return
	}
	if n.notices.waiting == nil {
		n.notices.waiting = map[string]waitingNotice{}
	}
	n.notices.waiting[thread] = w
	for len(n.notices.waiting) > noticesKept {
		oldest := ""
		for t, x := range n.notices.waiting {
			if oldest == "" || x.at.Before(n.notices.waiting[oldest].at) {
				oldest = t
			}
		}
		delete(n.notices.waiting, oldest)
	}
}

// dropWaitingNotice forgets the thread's waiting notice if it is of kind.
func (n *Machine) dropWaitingNotice(thread, kind string) {
	n.notices.mu.Lock()
	defer n.notices.mu.Unlock()
	if w, ok := n.notices.waiting[thread]; ok && w.kind == kind {
		delete(n.notices.waiting, thread)
	}
}

// sendWaitingNotices hands the relay the notices that waited for a
// connection, oldest first; those it does not take wait on.
func (n *Machine) sendWaitingNotices(conn *transport.RelayConn) {
	n.notices.mu.Lock()
	waiting := n.notices.waiting
	n.notices.waiting = nil
	n.notices.mu.Unlock()
	threads := slices.SortedFunc(maps.Keys(waiting), func(a, b string) int { return waiting[a].at.Compare(waiting[b].at) })
	for i, t := range threads {
		w := waiting[t]
		if time.Since(w.at) > noticeKept {
			continue
		}
		if err := n.handNotice(conn, w); err != nil {
			n.log.Debug("notices kept for later", "count", len(threads)-i, "err", err)
			for _, t := range threads[i:] {
				n.keepNotice(t, waiting[t])
			}
			return
		}
	}
}

// trimQuestion keeps a question small enough for a notification (the push
// service takes 4 KB): the text and options a lock screen can show.
func trimQuestion(q *sessionv1.Question) *sessionv1.Question {
	if q == nil {
		return nil
	}
	q = proto.Clone(q).(*sessionv1.Question)
	if len(q.Items) > 3 {
		q.Items = q.Items[:3]
	}
	for _, it := range q.Items {
		it.Text, _ = rt.Truncate(it.Text, 500)
		it.Header, _ = rt.Truncate(it.Header, 60)
		if len(it.Options) > 6 {
			it.Options = it.Options[:6]
		}
		for _, o := range it.Options {
			o.Label, _ = rt.Truncate(o.Label, 60)
		}
	}
	return q
}
