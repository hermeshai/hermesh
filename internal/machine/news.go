package machine

import (
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
	"github.com/hermeshai/hermesh/internal/provider"
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
func (n *Machine) sessionNews(s noticeSession, e *sessionv1.SessionEvent) {
	to := []*inboxv1.Address{{Inbox: s.asker, Session: s.askerConv}}
	if s.createdBy != s.asker {
		to = append(to, &inboxv1.Address{Inbox: s.createdBy, Session: s.creatorConv})
	}
	for _, a := range to {
		if strings.HasPrefix(a.Inbox, "c_") && !n.sessions.following(s.id, a.Inbox) {
			n.newsFor(a, s, e)
		}
	}
}

// newsFor writes one agent the news of event e.
func (n *Machine) newsFor(to *inboxv1.Address, s noticeSession, e *sessionv1.SessionEvent) {
	news := &inboxv1.SessionNews{SessionId: s.id, Title: s.title, Project: s.project, Agent: s.agent, Machine: n.Name()}
	switch k := e.Kind.(type) {
	case *sessionv1.SessionEvent_QuestionAsked:
		news.Kind, news.Question, news.Turn = "question", k.QuestionAsked.Question, k.QuestionAsked.Question.GetTurn()
	case *sessionv1.SessionEvent_TurnCompleted:
		tc := k.TurnCompleted
		news.Turn = tc.Turn
		if f := tc.Failure; f != nil {
			if f.Kind == sessionv1.FailureKind_FAILURE_KIND_INTERRUPTED {
				return
			}
			news.Kind, news.Failure = "failure", f.Kind
			news.FailureMessage, _ = provider.Truncate(f.Message, 1000)
		} else {
			news.Kind, news.DiffStat = "turn_done", tc.DiffStat
			news.Result, news.ResultTruncated = provider.Truncate(tc.Result, newsResultChars)
		}
	case *sessionv1.SessionEvent_Status:
		if k.Status.Status != sessionv1.SessionStatus_SESSION_STATUS_CRASHED {
			return
		}
		news.Kind, news.Failure = "failure", sessionv1.FailureKind_FAILURE_KIND_AGENT_CRASHED
	default:
		return
	}
	m := &inboxv1.Message{MessageId: "msg_" + strings.ToLower(ulid.Make().String()), From: &inboxv1.Address{Inbox: n.machineID, Session: s.id},
		To: to, SentAt: timestamppb.Now(), Text: newsText(news), SessionNews: news}
	for attempt := range 3 {
		err := n.sendMessage(m)
		if err == nil {
			return
		}
		n.log.Debug("session news not sent", "session", s.id, "to", to.Inbox, "attempt", attempt, "err", err)
		time.Sleep(time.Duration(5*(attempt+1)) * time.Second)
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
