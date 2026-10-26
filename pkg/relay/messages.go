package relay

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// The post office (docs/agent-messages.md §5): the owner's agents and
// machines each have an inbox, and the relay keeps the messages sealed to
// them until they are acknowledged, at most MessageRetention. It routes on
// the inbox and never opens a message; it keeps neither who sent one nor
// which session it is for.

const (
	// MessageRetention is how long a message waits, and how long its state
	// is remembered after it is gone.
	MessageRetention = 30 * 24 * time.Hour
	maxSealedMessage = 256 << 10
	maxInboxMessages = 1000
	maxInboxBytes    = 16 << 20
	maxFetch         = 50
	// Per sender, and from one inbox to another: two agents that answer
	// each other's every message stop at the second.
	messagesPerMinute = 60
	pairPerHour       = 30
)

var messageIDPattern = regexp.MustCompile(`^msg_[0-9a-z]{26}$`)

// Message is a sealed message at the post office.
type Message struct {
	ID         string
	Inbox      string
	Sealed     []byte
	ReceivedAt time.Time
	ExpiresAt  time.Time
}

// MessageState is what became of a message: "waiting", "taken", "handled",
// "held", "refused" or "expired".
type MessageState struct {
	State string
	At    time.Time
}

// MessageStore keeps the post office. A Backend may implement it (the
// SQLite store does); otherwise it lives in memory until the relay stops.
type MessageStore interface {
	PutMessage(ownerID string, m Message) error
	// WaitingMessages are an inbox's unacknowledged messages, oldest first.
	WaitingMessages(ownerID, inbox string) ([]Message, error)
	// SetMessageStates records what became of messages of an inbox:
	// "taken" keeps a message waiting, any other state drops its body.
	SetMessageStates(ownerID, inbox string, ids []string, state string, at time.Time) error
	MessageStates(ownerID string, ids []string) (map[string]MessageState, error)
	// ExpireMessages drops bodies past their expiry (state "expired") and
	// forgets states a retention later.
	ExpireMessages(now time.Time) error
	// DropInbox forgets an inbox that ended: its messages and registration.
	DropInbox(ownerID, inbox string) error
	PutRegistration(ownerID, inbox string, reg *identityv1.Signed) error
	Registrations(ownerID string) (map[string]*identityv1.Signed, error)
	// PutCard keeps an agent's card with the cert of the device that signed it.
	PutCard(ownerID, clientID string, card, deviceCert *identityv1.Signed) error
	Cards(ownerID string) (map[string]StoredCard, error)
}

// StoredCard is an agent's card and the cert of the device that signed it.
type StoredCard struct {
	Card, DeviceCert *identityv1.Signed
}

func (s *Server) messageStore() MessageStore {
	if ms, ok := s.store.(MessageStore); ok {
		return ms
	}
	return &s.messageMem
}

// SetMessageHook tells f the owner and inbox of every message the post
// office takes in: the MCP gateway rings its connections' webhooks.
func (s *Server) SetMessageHook(f func(ownerID, inbox string)) {
	s.mu.Lock()
	s.messageHook = f
	s.mu.Unlock()
}

// InboxWaiting counts the messages waiting in an inbox.
func (s *Server) InboxWaiting(ownerID, inbox string) int {
	ms, err := s.messageStore().WaitingMessages(ownerID, inbox)
	if err != nil {
		return 0
	}
	return len(ms)
}

type messageService struct{ s *Server }

func (m *messageService) Register(ctx context.Context, req *relayv1.RegisterInboxRequest) (*relayv1.RegisterInboxResponse, error) {
	p := peerFrom(ctx)
	reg := &identityv1.InboxRegistration{}
	if err := identity.VerifyWith(req.Registration, p.subject.PublicKey, reg); err != nil {
		return nil, errf(connect.CodeInvalidArgument, "inbox registration: "+err.Error())
	}
	if reg.OwnerId != p.t.ownerID || reg.Inbox != p.subject.ID || len(reg.InboxPublicKey) != 32 || len(reg.TakesMessages) > 16 {
		return nil, errf(connect.CodeInvalidArgument, "an inbox registers itself, with a 32-byte key")
	}
	if err := m.s.messageStore().PutRegistration(p.t.ownerID, reg.Inbox, req.Registration); err != nil {
		return nil, err
	}
	return &relayv1.RegisterInboxResponse{}, nil
}

func (m *messageService) Contacts(ctx context.Context, _ *relayv1.ContactsRequest) (*relayv1.ContactsResponse, error) {
	t := peerFrom(ctx).t
	contacts, err := m.s.contacts(t)
	if err != nil {
		return nil, err
	}
	return &relayv1.ContactsResponse{Contacts: contacts, RootChain: t.trust.RootChain()}, nil
}

// contacts lists the owner's inboxes: agents whose grant stands and whose
// card verifies, and machines.
func (s *Server) contacts(t *tenant) ([]*identityv1.Contact, error) {
	ms := s.messageStore()
	regs, err := ms.Registrations(t.ownerID)
	if err != nil {
		return nil, err
	}
	cards, err := ms.Cards(t.ownerID)
	if err != nil {
		return nil, err
	}
	var out []*identityv1.Contact
	for clientID := range s.liveClients(t) {
		c, ok := cards[clientID]
		if !ok {
			continue
		}
		if _, _, err := t.trust.VerifyCard(c.Card, c.DeviceCert); err != nil {
			continue
		}
		out = append(out, &identityv1.Contact{Card: c.Card, DeviceCert: c.DeviceCert, Registration: regs[clientID]})
	}
	certs, err := s.store.MachineCerts(t.ownerID)
	if err != nil {
		return nil, err
	}
	for id, cert := range certs {
		out = append(out, &identityv1.Contact{MachineCert: cert, Registration: regs[id]})
	}
	return out, nil
}

// liveClients are the owner's agents whose grant is not revoked.
func (s *Server) liveClients(t *tenant) map[string]bool {
	entries, err := s.clientEntries(t)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, e := range entries {
		g := &identityv1.Grant{}
		if identity.Unverified(e.Grant, g) == nil {
			out[g.ClientId] = true
		}
	}
	return out
}

// isInbox reports whether inbox is one of the owner's: a live agent or a
// machine.
func (s *Server) isInbox(t *tenant, inbox string) bool {
	if s.liveClients(t)[inbox] {
		return true
	}
	certs, err := s.store.MachineCerts(t.ownerID)
	return err == nil && certs[inbox] != nil
}

func (m *messageService) Send(ctx context.Context, req *relayv1.SendSealedRequest) (*relayv1.SendSealedResponse, error) {
	s := m.s
	p := peerFrom(ctx)
	t := p.t
	sm := req.Message
	switch {
	case sm == nil || !messageIDPattern.MatchString(sm.MessageId) || len(sm.Sealed) == 0:
		return nil, errf(connect.CodeInvalidArgument, "a message has an id (msg_ and a ULID) and a sealed body")
	case len(sm.Sealed) > maxSealedMessage:
		return nil, errf(connect.CodeInvalidArgument, "message too large")
	case !s.isInbox(t, sm.Inbox):
		return nil, errf(connect.CodeNotFound, "no such inbox: "+sm.Inbox)
	}
	s.maybeExpire()
	ms := s.messageStore()
	if st, err := ms.MessageStates(t.ownerID, []string{sm.MessageId}); err == nil && st[sm.MessageId].State != "" {
		return &relayv1.SendSealedResponse{}, nil // sent again: the post office has it
	}
	if !s.messageLimit.Allow(t.ownerID+"/"+p.subject.ID) || !s.pairLimit.Allow(t.ownerID+"/"+p.subject.ID+"/"+sm.Inbox) {
		return nil, errf(connect.CodeResourceExhausted, "too many messages: wait a while, or put them in one")
	}
	waiting, err := ms.WaitingMessages(t.ownerID, sm.Inbox)
	if err != nil {
		return nil, err
	}
	size := len(sm.Sealed)
	for _, w := range waiting {
		size += len(w.Sealed)
	}
	if len(waiting) >= maxInboxMessages || size > maxInboxBytes {
		return nil, connect.NewError(connect.CodeResourceExhausted, errInboxFull)
	}
	now := time.Now()
	if err := ms.PutMessage(t.ownerID, Message{ID: sm.MessageId, Inbox: sm.Inbox, Sealed: sm.Sealed, ReceivedAt: now, ExpiresAt: now.Add(MessageRetention)}); err != nil {
		return nil, err
	}
	s.nudgeInbox(t, sm.Inbox)
	return &relayv1.SendSealedResponse{}, nil
}

var errInboxFull = errors.New("inbox_full: the recipient's inbox is full until it takes its messages")

// nudgeInbox wakes the inbox's watchers and the message hook.
func (s *Server) nudgeInbox(t *tenant, inbox string) {
	s.mu.Lock()
	for ch := range t.inboxWatchers[inbox] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	hook := s.messageHook
	s.mu.Unlock()
	if hook != nil {
		go hook(t.ownerID, inbox)
	}
}

func (m *messageService) Fetch(ctx context.Context, req *relayv1.FetchMessagesRequest) (*relayv1.FetchMessagesResponse, error) {
	p := peerFrom(ctx)
	m.s.maybeExpire()
	ms := m.s.messageStore()
	waiting, err := ms.WaitingMessages(p.t.ownerID, p.subject.ID)
	if err != nil {
		return nil, err
	}
	limit := int(req.Limit)
	if limit <= 0 || limit > maxFetch {
		limit = maxFetch
	}
	res := &relayv1.FetchMessagesResponse{More: len(waiting) > limit}
	var ids []string
	for _, w := range waiting[:min(limit, len(waiting))] {
		res.Messages = append(res.Messages, &relayv1.SealedMessage{Inbox: w.Inbox, MessageId: w.ID, Sealed: w.Sealed, ReceivedAt: timestamppb.New(w.ReceivedAt)})
		ids = append(ids, w.ID)
	}
	if len(ids) > 0 {
		if err := ms.SetMessageStates(p.t.ownerID, p.subject.ID, ids, "taken", time.Now()); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func (m *messageService) Ack(ctx context.Context, req *relayv1.AckMessagesRequest) (*relayv1.AckMessagesResponse, error) {
	p := peerFrom(ctx)
	byOutcome := map[string][]string{}
	for _, a := range req.Acks {
		switch a.Outcome {
		case "handled", "held", "refused":
			byOutcome[a.Outcome] = append(byOutcome[a.Outcome], a.MessageId)
		default:
			return nil, errf(connect.CodeInvalidArgument, "an outcome is handled, held or refused")
		}
	}
	now := time.Now()
	for outcome, ids := range byOutcome {
		if err := m.s.messageStore().SetMessageStates(p.t.ownerID, p.subject.ID, ids, outcome, now); err != nil {
			return nil, err
		}
	}
	return &relayv1.AckMessagesResponse{}, nil
}

func (m *messageService) Watch(ctx context.Context, _ *relayv1.WatchInboxRequest, stream *connect.ServerStream[relayv1.WatchInboxResponse]) error {
	s := m.s
	p := peerFrom(ctx)
	t, inbox := p.t, p.subject.ID
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	if t.inboxWatchers == nil {
		t.inboxWatchers = map[string]map[chan struct{}]struct{}{}
	}
	if t.inboxWatchers[inbox] == nil {
		t.inboxWatchers[inbox] = map[chan struct{}]struct{}{}
	}
	t.inboxWatchers[inbox][ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(t.inboxWatchers[inbox], ch)
		s.mu.Unlock()
	}()
	for {
		if err := stream.Send(&relayv1.WatchInboxResponse{Waiting: uint32(s.InboxWaiting(t.ownerID, inbox))}); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ch:
		}
	}
}

func (m *messageService) Status(ctx context.Context, req *relayv1.MessageStatusRequest) (*relayv1.MessageStatusResponse, error) {
	p := peerFrom(ctx)
	if len(req.MessageIds) > 100 {
		return nil, errf(connect.CodeInvalidArgument, "at most 100 ids at once")
	}
	states, err := m.s.messageStore().MessageStates(p.t.ownerID, req.MessageIds)
	if err != nil {
		return nil, err
	}
	res := &relayv1.MessageStatusResponse{}
	for _, id := range req.MessageIds {
		st, ok := states[id]
		ms := &relayv1.MessageState{MessageId: id, State: "unknown"}
		if ok {
			ms.State, ms.At = st.State, timestamppb.New(st.At)
		}
		res.States = append(res.States, ms)
	}
	return res, nil
}

// PublishCards keeps the cards a trusted device signed for the owner's
// agents.
func (o *ownerControl) PublishCards(ctx context.Context, req *relayv1.PublishCardsRequest) (*relayv1.PublishCardsResponse, error) {
	p := peerFrom(ctx)
	t := p.t
	if p.deviceCert == nil {
		return nil, errf(connect.CodePermissionDenied, "only a trusted device signs cards")
	}
	live := o.s.liveClients(t)
	for _, card := range req.Cards {
		c, _, err := t.trust.VerifyCard(card, p.deviceCert)
		if err != nil {
			return nil, errf(connect.CodeInvalidArgument, err.Error())
		}
		if !live[c.ClientId] {
			continue // revoked meanwhile
		}
		if len(c.Name) > 128 || (c.Kind != "mcp" && c.Kind != "cli") {
			return nil, errf(connect.CodeInvalidArgument, "bad card")
		}
		if err := o.s.messageStore().PutCard(t.ownerID, c.ClientId, card, p.deviceCert); err != nil {
			return nil, err
		}
	}
	o.s.wakeOwner(t)
	return &relayv1.PublishCardsResponse{}, nil
}

// maybeExpire drops expired messages, at most once an hour: the post office
// is swept as it is used.
func (s *Server) maybeExpire() {
	now := time.Now()
	s.mu.Lock()
	due := now.Sub(s.expiredAt) > time.Hour
	if due {
		s.expiredAt = now
	}
	s.mu.Unlock()
	if !due {
		return
	}
	if err := s.messageStore().ExpireMessages(now); err != nil {
		s.log.Warn("expiring messages", "err", err)
	}
}

// dropInboxes forgets the inboxes of revoked agents and removed machines.
func (s *Server) dropInboxes(t *tenant, clients, machines []string) {
	for _, id := range slices.Concat(clients, machines) {
		if err := s.messageStore().DropInbox(t.ownerID, id); err != nil {
			s.log.Warn("dropping an inbox", "owner", t.ownerID, "inbox", id, "err", err)
		}
	}
}

// memoryMessages is the MessageStore of a Backend without one.
type memoryMessages struct {
	mu    sync.Mutex
	msgs  map[string]map[string]*memMessage // owner → id
	regs  map[string]map[string]*identityv1.Signed
	cards map[string]map[string]StoredCard
}

type memMessage struct {
	Message
	state MessageState
}

func (m *memoryMessages) init(ownerID string) {
	if m.msgs == nil {
		m.msgs, m.regs, m.cards = map[string]map[string]*memMessage{}, map[string]map[string]*identityv1.Signed{}, map[string]map[string]StoredCard{}
	}
	if m.msgs[ownerID] == nil {
		m.msgs[ownerID], m.regs[ownerID], m.cards[ownerID] = map[string]*memMessage{}, map[string]*identityv1.Signed{}, map[string]StoredCard{}
	}
}

func (m *memoryMessages) PutMessage(ownerID string, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init(ownerID)
	if m.msgs[ownerID][msg.ID] == nil { // sent again: the first one stays
		m.msgs[ownerID][msg.ID] = &memMessage{Message: msg, state: MessageState{State: "waiting", At: msg.ReceivedAt}}
	}
	return nil
}

func (m *memoryMessages) WaitingMessages(ownerID, inbox string) ([]Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Message
	for _, msg := range m.msgs[ownerID] {
		if msg.Inbox == inbox && msg.Sealed != nil {
			out = append(out, msg.Message)
		}
	}
	slices.SortFunc(out, func(a, b Message) int {
		if c := a.ReceivedAt.Compare(b.ReceivedAt); c != 0 {
			return c
		}
		return compareStrings(a.ID, b.ID)
	})
	return out, nil
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (m *memoryMessages) SetMessageStates(ownerID, inbox string, ids []string, state string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		msg := m.msgs[ownerID][id]
		if msg == nil || msg.Inbox != inbox || msg.Sealed == nil {
			continue
		}
		msg.state = MessageState{State: state, At: at}
		if state != "taken" {
			msg.Sealed = nil
		}
	}
	return nil
}

func (m *memoryMessages) MessageStates(ownerID string, ids []string) (map[string]MessageState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]MessageState{}
	for _, id := range ids {
		if msg := m.msgs[ownerID][id]; msg != nil {
			out[id] = msg.state
		}
	}
	return out, nil
}

func (m *memoryMessages) ExpireMessages(now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, byID := range m.msgs {
		for id, msg := range byID {
			switch {
			case now.After(msg.ExpiresAt.Add(MessageRetention)):
				delete(byID, id)
			case msg.Sealed != nil && now.After(msg.ExpiresAt):
				msg.Sealed, msg.state = nil, MessageState{State: "expired", At: now}
			}
		}
	}
	return nil
}

func (m *memoryMessages) DropInbox(ownerID, inbox string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.msgs[ownerID] {
		if msg.Inbox == inbox && msg.Sealed != nil {
			msg.Sealed, msg.state = nil, MessageState{State: "expired", At: time.Now()}
		}
	}
	delete(m.regs[ownerID], inbox)
	delete(m.cards[ownerID], inbox)
	return nil
}

func (m *memoryMessages) PutRegistration(ownerID, inbox string, reg *identityv1.Signed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init(ownerID)
	m.regs[ownerID][inbox] = reg
	return nil
}

func (m *memoryMessages) Registrations(ownerID string) (map[string]*identityv1.Signed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]*identityv1.Signed{}
	for k, v := range m.regs[ownerID] {
		out[k] = v
	}
	return out, nil
}

func (m *memoryMessages) PutCard(ownerID, clientID string, card, deviceCert *identityv1.Signed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init(ownerID)
	m.cards[ownerID][clientID] = StoredCard{Card: card, DeviceCert: deviceCert}
	return nil
}

func (m *memoryMessages) Cards(ownerID string) (map[string]StoredCard, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]StoredCard{}
	for k, v := range m.cards[ownerID] {
		out[k] = v
	}
	return out, nil
}
