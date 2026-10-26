package machine

import (
	"bytes"
	"context"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Notices for the owner's iPhones. When a session asks something, fails or
// finishes a turn, the machine seals a notice to the notice key of each
// trusted device that asked for that kind (its push registration, signed by
// its device key, which a root key certifies) and hands the ciphertext to
// the relay for the push service. The relay never sees what it says.

// pushTarget is a verified push registration.
type pushTarget struct {
	keyID string
	key   []byte
	kinds map[string]bool
}

// noticeSession is what a notice says about its session, copied while the
// session is locked.
type noticeSession struct {
	id, title, createdBy, project, agent string
	// asker asked for the turn the notice is about; askerConv and
	// creatorConv are where news goes for the asker and the creator.
	asker, askerConv, creatorConv string
}

// notices keeps the push targets and what was last sent per session.
type notices struct {
	mu      sync.Mutex
	targets []pushTarget
	// sent remembers the last notice of each session, so a crash after a
	// failed turn is not announced twice.
	sent map[string]string
}

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

// sessionNotice tells the owner's devices of event e, if it is one they
// asked for.
func (n *Machine) sessionNotice(s noticeSession, e *sessionv1.SessionEvent) {
	nt := &sessionv1.Notice{At: timestamppb.Now(), SessionId: s.id, Title: s.title, Project: s.project, Agent: s.agent}
	switch k := e.Kind.(type) {
	case *sessionv1.SessionEvent_QuestionAsked:
		nt.Kind = "question"
		nt.Question = trimQuestion(k.QuestionAsked.Question)
	case *sessionv1.SessionEvent_TurnCompleted:
		tc := k.TurnCompleted
		nt.Turn = tc.Turn
		if f := tc.Failure; f != nil {
			if f.Kind == sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED {
				return
			}
			nt.Kind = "failure"
			nt.Failure = f.Kind
			nt.FailureMessage, _ = provider.Truncate(f.Message, 300)
		} else {
			nt.Kind = "turn_done"
			nt.DiffStat = tc.DiffStat
		}
	case *sessionv1.SessionEvent_Status:
		if k.Status.Status != sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
			return
		}
		nt.Kind = "failure"
		nt.Failure = sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED
	default:
		return
	}
	n.notices.mu.Lock()
	targets := n.notices.targets
	if n.notices.sent == nil {
		n.notices.sent = map[string]string{}
	}
	if nt.Kind == "failure" && n.notices.sent[s.id] == "failure" && e.GetStatus() != nil {
		n.notices.mu.Unlock()
		return
	}
	n.notices.sent[s.id] = nt.Kind
	n.notices.mu.Unlock()
	var out []*relayv1.SealedNotice
	for _, t := range targets {
		if !t.kinds[nt.Kind] {
			continue
		}
		if len(out) == 0 {
			nt.Grant = n.knownClients()[s.createdBy].Label
			nt.Machine = n.Name()
		}
		sealed, err := identity.SealNotice(t.key, nt)
		if err != nil {
			continue
		}
		out = append(out, &relayv1.SealedNotice{DeviceKeyId: t.keyID, Sealed: sealed, Kind: nt.Kind, Thread: s.id})
	}
	n.mu.Lock()
	conn := n.conn
	n.mu.Unlock()
	if len(out) == 0 {
		return
	}
	if conn == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c, err := n.control(conn)
		if err == nil {
			_, err = c.Notify(ctx, &relayv1.NotifyRequest{Notices: out})
		}
		if err != nil {
			n.log.Debug("notice not sent", "session", s.id, "err", err)
		}
	}()
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
		it.Text, _ = provider.Truncate(it.Text, 500)
		it.Header, _ = provider.Truncate(it.Header, 60)
		if len(it.Options) > 6 {
			it.Options = it.Options[:6]
		}
		for _, o := range it.Options {
			o.Label, _ = provider.Truncate(o.Label, 60)
		}
	}
	return q
}
