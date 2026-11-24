package machine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	inboxv1 "github.com/hermeshai/hermesh/gen/go/hermesh/inbox/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	rt "github.com/hermeshai/hermesh/internal/runtime"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

// The machine's inbox, and news for the agents that start sessions on it
// (docs/agent-messages.md §7.3): when a turn ends, asks a question or
// fails, the machine writes the agent that asked for it a message from the
// session, sealed to that agent's inbox, unless the agent follows the
// session at that moment and so has the news already.

// newsResultChars is how much of a turn's result its news carries.
const newsResultChars = 2000

func inboxKeyPath(dataDir string) string { return filepath.Join(dataDir, "keys", "inbox.key") }

// inboxKey is the machine's inbox private key, made on first use.
func (n *Machine) inboxKey() ([]byte, error) {
	path := inboxKeyPath(n.cfg.DataDir)
	b, err := os.ReadFile(path)
	if err == nil && len(b) == 32 {
		return b, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	priv, _, err := identity.NewInboxKey()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return priv, os.WriteFile(path, priv, 0o600)
}

func (n *Machine) messageService(conn *transport.RelayConn) (relayv1connect.MessageServiceClient, error) {
	hc, err := conn.Control()
	if err != nil {
		return nil, err
	}
	return relayv1connect.NewMessageServiceClient(hc, "http://relay", transport.ClientNoCompression), nil
}

// registerInbox tells the post office the machine's inbox key, each time
// the machine connects.
func (n *Machine) registerInbox(ctx context.Context, conn *transport.RelayConn) {
	priv, err := n.inboxKey()
	if err != nil {
		n.log.Warn("inbox key", "err", err)
		return
	}
	pub, err := identity.InboxPublicKey(priv)
	if err != nil {
		return
	}
	reg, err := identity.SignRegistration(n.key, n.OwnerID(), n.machineID, pub, "session")
	if err != nil {
		return
	}
	ms, err := n.messageService(conn)
	if err == nil {
		_, err = ms.Register(ctx, &relayv1.RegisterInboxRequest{Registration: reg})
	}
	if err != nil {
		n.log.Debug("registering the inbox", "err", err)
	}
}

// contactBook caches the owner's contacts for a minute.
type contactBook struct {
	mu   sync.Mutex
	at   time.Time
	list []*identityv1.Contact
	// woken are the names with a doorbell, by inbox (docs/agent-names.md §7.1).
	woken map[string][]string
}

// keep remembers the owner's contacts as the post office gave them.
func (b *contactBook) keep(res *relayv1.ContactsResponse) {
	b.list, b.at, b.woken = res.Contacts, time.Now(), map[string][]string{}
	for _, w := range res.Woken {
		b.woken[w.Inbox] = w.Names
	}
}

// isWoken reports whether a message to an address rings a doorbell: its
// own, or its agent's, which wakes it for every name without one.
func (n *Machine) isWoken(a *inboxv1.Address) bool {
	n.contacts.mu.Lock()
	defer n.contacts.mu.Unlock()
	woken := n.contacts.woken[a.GetInbox()]
	return slices.Contains(woken, a.GetSession()) || slices.Contains(woken, "")
}

// contact finds an inbox among the owner's contacts and checks it.
func (n *Machine) contact(ctx context.Context, ms relayv1connect.MessageServiceClient, inbox string) (*identity.ContactInfo, error) {
	n.contacts.mu.Lock()
	defer n.contacts.mu.Unlock()
	find := func() *identity.ContactInfo {
		for _, c := range n.contacts.list {
			if info, err := n.trust.VerifyContact(c); err == nil && info.Inbox == inbox {
				return info
			}
		}
		return nil
	}
	if info := find(); info != nil && info.InboxKey != nil && time.Since(n.contacts.at) < time.Minute {
		return info, nil
	}
	res, err := ms.Contacts(ctx, &relayv1.ContactsRequest{})
	if err != nil {
		return nil, err
	}
	n.contacts.keep(res)
	info := find()
	if info == nil {
		return nil, fmt.Errorf("%s is not among the owner's contacts", inbox)
	}
	if info.InboxKey == nil {
		return nil, fmt.Errorf("%s has no inbox yet", inbox)
	}
	return info, nil
}

// sessionNews writes the agent that asked for a turn how it went, and the
// session's creator when that's someone else, for the events the owner's
// iPhone hears of too.
func (n *Machine) sessionNews(sess *sessionv1.Session, ev *sessionv1.SessionEvent) {
	if sess == nil || ev == nil {
		return
	}
	switch ev.Type {
	case "request.created":
		if ev.GetRequest().GetKind() != sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT {
			return
		}
	case "run.completed", "run.failed":
	case "session.recovered":
		if sess.Status != sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
			return
		}
	default:
		return
	}

	createdBy := cmp.Or(sess.CreatedBy, sess.GetActor().GetId())
	creatorConv := sess.CreatedAs
	asker := ev.GetRun().GetAuthor().GetId()
	askerConv := ev.GetRun().GetAuthorConversation()
	if asker == "" && n.sessions != nil {
		runID := cmp.Or(ev.GetRequest().GetRunId(), sess.ActiveRunId)
		if runID != "" {
			if r, err := n.sessions.Store().Run(runID); err == nil && r != nil {
				asker = r.GetAuthor().GetId()
				askerConv = r.GetAuthorConversation()
			}
		}
	}
	if asker == "" {
		asker, askerConv = createdBy, creatorConv
	}

	to := []*inboxv1.Address{{Inbox: asker, Session: askerConv}}
	if createdBy != "" && createdBy != asker {
		to = append(to, &inboxv1.Address{Inbox: createdBy, Session: creatorConv})
	}
	for _, a := range to {
		if strings.HasPrefix(a.Inbox, "c_") && (n.sessions == nil || !n.sessions.Following(sess.SessionId, a.Inbox)) {
			m := n.newsMessage(a, sess, ev)
			if m == nil {
				continue
			}
			go n.deliverNews(a, sess.SessionId, m)
		}
	}
}

func (n *Machine) deliverNews(to *inboxv1.Address, sessionID string, m *inboxv1.Message) {
	if n.herTakes(m) {
		return
	}
	for attempt := range 3 {
		err := n.sendMessage(m)
		if err == nil {
			return
		}
		n.log.Debug("session news not sent", "session", sessionID, "to", to.Inbox, "attempt", attempt, "err", err)
		time.Sleep(time.Duration(5*(attempt+1)) * time.Second)
	}
}

// newsMessage is the news of event ev for one agent; nil when ev is none.
func (n *Machine) newsMessage(to *inboxv1.Address, sess *sessionv1.Session, ev *sessionv1.SessionEvent) *inboxv1.Message {
	agent := cmp.Or(sess.Agent, sess.GetModelSelection().GetInstanceId())
	news := &inboxv1.SessionNews{
		SessionId: sess.SessionId,
		Title:     sess.Title,
		Project:   sess.ProjectId,
		Agent:     agent,
		Machine:   n.Name(),
	}
	switch ev.Type {
	case "request.created":
		if ev.GetRequest().GetKind() != sessionv1.RuntimeRequestKind_RUNTIME_REQUEST_KIND_USER_INPUT {
			return nil
		}
		q := sess.GetProgress().GetPendingQuestion()
		if q == nil {
			return nil
		}
		news.Kind, news.Question, news.Turn = "question", q, q.GetTurn()
	case "run.completed":
		news.Kind = "turn_done"
		news.Turn = cmp.Or(ev.GetRun().GetOrdinal(), sess.GetProgress().GetTurnCount())
		news.DiffStat = sess.GetProgress().GetDiffStat()
		news.Result, news.ResultTruncated = rt.Truncate(sess.GetResult(), newsResultChars)
	case "run.failed":
		f := sess.GetProgress().GetLastFailure()
		if f != nil && f.Kind == sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED {
			return nil
		}
		news.Kind = "failure"
		news.Turn = cmp.Or(ev.GetRun().GetOrdinal(), sess.GetProgress().GetTurnCount())
		news.Failure = f.GetKind()
		news.FailureMessage, _ = rt.Truncate(cmp.Or(f.GetMessage(), sess.GetError()), 1000)
	case "session.recovered":
		if sess.Status != sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
			return nil
		}
		f := sess.GetProgress().GetLastFailure()
		news.Kind = "failure"
		news.Turn = sess.GetProgress().GetTurnCount()
		news.Failure = f.GetKind()
		news.FailureMessage, _ = rt.Truncate(cmp.Or(f.GetMessage(), sess.GetError()), 1000)
	default:
		return nil
	}
	return &inboxv1.Message{
		MessageId:   "msg_" + strings.ToLower(ulid.Make().String()),
		From:        &inboxv1.Address{Inbox: n.machineID, Session: sess.SessionId},
		To:          to,
		SentAt:      timestamppb.Now(),
		Text:        newsText(news),
		SessionNews: news,
	}
}

// sendMessage signs m with the machine key, seals it to its recipient and
// hands it to the post office.
func (n *Machine) sendMessage(m *inboxv1.Message) error {
	n.mu.Lock()
	conn, cert := n.conn, n.machineCert
	n.mu.Unlock()
	if conn == nil || cert == nil {
		return errors.New("not connected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ms, err := n.messageService(conn)
	if err != nil {
		return err
	}
	to, err := n.contact(ctx, ms, m.To.Inbox)
	if err != nil {
		return err
	}
	signed, err := identity.Sign(m, n.key)
	if err != nil {
		return err
	}
	sealed, err := identity.SealMessage(to.InboxKey, to.Inbox, m.MessageId, &inboxv1.SignedMessage{Message: signed, Sender: &identityv1.Contact{MachineCert: cert}})
	if err != nil {
		return err
	}
	sm := &relayv1.SealedMessage{Inbox: to.Inbox, MessageId: m.MessageId, Sealed: sealed}
	if n.isWoken(m.To) {
		ring := m.To.GetSession()
		sm.Ring = &ring
	}
	_, err = ms.Send(ctx, &relayv1.SendSealedRequest{Message: sm})
	return err
}

// newsText says what a news message says, for a model to read.
func newsText(n *inboxv1.SessionNews) string {
	var b strings.Builder
	switch n.Kind {
	case "turn_done":
		fmt.Fprintf(&b, "Turn %d of session %s finished", n.Turn, n.SessionId)
		if d := n.DiffStat; d.GetFilesChanged() > 0 {
			fmt.Fprintf(&b, " (%d files changed, +%d −%d)", d.FilesChanged, d.Insertions, d.Deletions)
		}
		b.WriteString(".")
		if n.Result != "" {
			b.WriteString("\n\n" + n.Result)
			if n.ResultTruncated {
				b.WriteString("…")
			}
		}
		fmt.Fprintf(&b, "\n\nThe whole result: `hermesh session get %s`.", n.SessionId)
	case "question":
		fmt.Fprintf(&b, "Session %s asks:", n.SessionId)
		for _, it := range n.Question.GetItems() {
			b.WriteString("\n\n" + it.Text)
			for i, o := range it.Options {
				fmt.Fprintf(&b, "\n%d. %s", i+1, o.Label)
			}
		}
		fmt.Fprintf(&b, "\n\nAnswer with `hermesh session answer %s` (--choose, --text or --decline).", n.SessionId)
	case "failure":
		fmt.Fprintf(&b, "Turn %d of session %s failed: %s", n.Turn, n.SessionId, strings.ToLower(strings.TrimPrefix(n.Failure.String(), "FAILURE_KIND_")))
		if n.FailureMessage != "" {
			b.WriteString(": " + n.FailureMessage)
		}
		fmt.Fprintf(&b, "\n\nSee `hermesh session get %s`.", n.SessionId)
	}
	return b.String()
}
