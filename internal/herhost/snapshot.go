package herhost

import (
	"encoding/json"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
)

// What HerStatus says of her while it runs, kept current from the monitor's
// connection: bots/subscribe and bots/changed for the unread count and the
// newest message, models/list for whether a model is ready.

// bot is what the snapshot keeps of her's BotSummary.
type bot struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Unread  uint32  `json:"unread"`
	Waiting *string `json:"waiting"`
	Muted   bool    `json:"muted"`
	Last    *struct {
		From string `json:"from"`
		Text string `json:"text"`
		File string `json:"file"`
		Ts   string `json:"ts"`
		// Question: it asks the owner, with send_to_user's question.
		Question bool `json:"question"`
	} `json:"last"`
	// At work: a turn, sub-agents, a routine's run.
	Typing        bool      `json:"typing"`
	AgentsWorking uint32    `json:"agents_working"`
	Routines      []routine `json:"routines"`
}

// routine is what the snapshot keeps of her's RoutineSummary.
type routine struct {
	Running bool `json:"running"`
}

type snapshot struct {
	version    string
	modelReady bool
	bots       map[string]bot
	latest     *machinev1.HerLatest
}

// latestText is how much of a message HerLatest carries, in characters.
const latestText = 200

func newSnapshot(version string, bots []bot) *snapshot {
	s := &snapshot{version: version, bots: map[string]bot{}}
	for _, b := range bots {
		s.bots[b.ID] = b
	}
	s.latest = s.newest()
	return s
}

// changed takes a bots/changed: what changed in it is the newest, whenever
// her says it was written. What an assistant waits for the owner to answer
// (waiting: an approval, or a secure card open, by Her's words with it,
// else its title) is the newest while it waits, and gives way to the newest
// message once it no longer does. It returns the newest when the owner's
// devices tell of it, as notices.ts herNotices does: it is another one, the
// unread count grew with it, and its assistant isn't muted.
func (s *snapshot) changed(bots []bot, deleted []string, now time.Time) *machinev1.HerLatest {
	before, unread := s.latest, s.unread()
	for _, b := range bots {
		old, had := s.bots[b.ID]
		s.bots[b.ID] = b
		switch {
		case b.Waiting != nil && (!had || old.Waiting == nil || *old.Waiting != *b.Waiting):
			s.latest = latest(b, machinev1.HerLatestKind_HER_LATEST_KIND_APPROVAL, *b.Waiting, now)
		case b.Last != nil && b.Last.From == "bot" && (!had || old.Last == nil || *old.Last != *b.Last):
			s.latest = lastMessage(b)
		case b.Waiting == nil && s.latest != nil && s.latest.BotId == b.ID && s.latest.Kind == machinev1.HerLatestKind_HER_LATEST_KIND_APPROVAL:
			s.latest = s.newest()
		case s.latest != nil && s.latest.BotId == b.ID && (s.latest.BotName != b.Name || s.latest.Muted != b.Muted):
			l := proto.Clone(s.latest).(*machinev1.HerLatest)
			l.BotName, l.Muted = b.Name, b.Muted
			s.latest = l
		}
	}
	for _, id := range deleted {
		delete(s.bots, id)
		if s.latest != nil && s.latest.BotId == id {
			s.latest = s.newest()
		}
	}
	l := s.latest
	if l == nil || l.Muted || s.unread() <= unread || before != nil && l.BotId == before.BotId && l.At.AsTime().Equal(before.At.AsTime()) && l.Text == before.Text {
		return nil
	}
	return proto.Clone(l).(*machinev1.HerLatest)
}

// newest is the newest message of an assistant's, by its time.
func (s *snapshot) newest() *machinev1.HerLatest {
	var out *machinev1.HerLatest
	for _, b := range s.bots {
		if b.Last == nil || b.Last.From != "bot" {
			continue
		}
		if m := lastMessage(b); out == nil || m.At.AsTime().After(out.At.AsTime()) {
			out = m
		}
	}
	return out
}

// busy counts the assistants at work, whose work restarting her would stop.
func (s *snapshot) busy() int {
	n := 0
	for _, b := range s.bots {
		if b.Typing || b.AgentsWorking > 0 || slices.ContainsFunc(b.Routines, func(r routine) bool { return r.Running }) {
			n++
		}
	}
	return n
}

func (s *snapshot) unread() uint32 {
	var n uint32
	for _, b := range s.bots {
		n += b.Unread
	}
	return n
}

// lastMessage is b's last message as HerLatest, a question as one; a message
// of files without words names its first file.
func lastMessage(b bot) *machinev1.HerLatest {
	at, err := time.Parse(time.RFC3339, b.Last.Ts)
	if err != nil {
		at = time.Now()
	}
	text := b.Last.Text
	if text == "" {
		text = b.Last.File
	}
	kind := machinev1.HerLatestKind_HER_LATEST_KIND_MESSAGE
	if b.Last.Question {
		kind = machinev1.HerLatestKind_HER_LATEST_KIND_QUESTION
	}
	return latest(b, kind, text, at)
}

func latest(b bot, kind machinev1.HerLatestKind, text string, at time.Time) *machinev1.HerLatest {
	if r := []rune(text); len(r) > latestText {
		text = string(r[:latestText])
	}
	return &machinev1.HerLatest{BotId: b.ID, BotName: b.Name, Text: text, At: timestamppb.New(at), Kind: kind, Muted: b.Muted}
}

// modelReady: whether models/list's result has a model whose provider's
// credentials are there.
func modelReady(result []byte) bool {
	var r struct {
		Models []struct {
			Available bool `json:"available"`
		} `json:"models"`
	}
	json.Unmarshal(result, &r)
	for _, m := range r.Models {
		if m.Available {
			return true
		}
	}
	return false
}
