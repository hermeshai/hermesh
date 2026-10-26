package relay

import (
	"path/filepath"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// The post office's stores, SQLite and memory, behave alike.
func TestMessageStores(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for name, ms := range map[string]MessageStore{"sqlite": st, "memory": &memoryMessages{}} {
		t.Run(name, func(t *testing.T) {
			const owner = "o_x"
			t0 := time.Now()
			put := func(id, inbox string, at time.Time) {
				if err := ms.PutMessage(owner, Message{ID: id, Inbox: inbox, Sealed: []byte(id), ReceivedAt: at, ExpiresAt: at.Add(MessageRetention)}); err != nil {
					t.Fatal(err)
				}
			}
			put("msg_b", "c_1", t0.Add(time.Second))
			put("msg_a", "c_1", t0)
			put("msg_c", "c_2", t0)
			put("msg_a", "c_1", t0.Add(time.Hour)) // sent again: kept once
			w, err := ms.WaitingMessages(owner, "c_1")
			if err != nil || len(w) != 2 || w[0].ID != "msg_a" || w[1].ID != "msg_b" || string(w[0].Sealed) != "msg_a" {
				t.Fatalf("waiting, oldest first: %+v %v", w, err)
			}
			// Taken: still waiting. Handled: gone, its state remembered.
			ms.SetMessageStates(owner, "c_1", []string{"msg_a"}, "taken", t0)
			if w, _ := ms.WaitingMessages(owner, "c_1"); len(w) != 2 {
				t.Fatalf("a taken message must keep waiting: %+v", w)
			}
			ms.SetMessageStates(owner, "c_2", []string{"msg_a"}, "handled", t0) // not c_2's
			ms.SetMessageStates(owner, "c_1", []string{"msg_a"}, "handled", t0)
			if w, _ := ms.WaitingMessages(owner, "c_1"); len(w) != 1 || w[0].ID != "msg_b" {
				t.Fatalf("after handling: %+v", w)
			}
			states, _ := ms.MessageStates(owner, []string{"msg_a", "msg_b", "msg_z"})
			if states["msg_a"].State != "handled" || states["msg_b"].State != "waiting" || states["msg_z"].State != "" {
				t.Fatalf("states: %+v", states)
			}
			// Expired after the retention, forgotten a retention later.
			ms.ExpireMessages(t0.Add(MessageRetention + 2*time.Second))
			if w, _ := ms.WaitingMessages(owner, "c_1"); len(w) != 0 {
				t.Fatalf("expired messages must not wait: %+v", w)
			}
			if states, _ := ms.MessageStates(owner, []string{"msg_b"}); states["msg_b"].State != "expired" {
				t.Fatalf("expired state: %+v", states)
			}
			ms.ExpireMessages(t0.Add(2*MessageRetention + 2*time.Second))
			if states, _ := ms.MessageStates(owner, []string{"msg_b"}); states["msg_b"].State != "" {
				t.Fatalf("states must be forgotten a retention later: %+v", states)
			}
			// An inbox that ends loses its messages, registration and card.
			reg, card := &identityv1.Signed{Type: "reg"}, &identityv1.Signed{Type: "card"}
			ms.PutRegistration(owner, "c_2", reg)
			ms.PutCard(owner, "c_2", card, &identityv1.Signed{Type: "dev"})
			if regs, _ := ms.Registrations(owner); regs["c_2"].GetType() != "reg" {
				t.Fatalf("registrations: %v", regs)
			}
			if cards, _ := ms.Cards(owner); cards["c_2"].Card.GetType() != "card" || cards["c_2"].DeviceCert.GetType() != "dev" {
				t.Fatalf("cards: %v", cards)
			}
			ms.DropInbox(owner, "c_2")
			regs, _ := ms.Registrations(owner)
			cards, _ := ms.Cards(owner)
			w, _ = ms.WaitingMessages(owner, "c_2")
			if regs["c_2"] != nil || cards["c_2"].Card != nil || len(w) != 0 {
				t.Fatalf("a dropped inbox: %v %v %v", regs, cards, w)
			}
		})
	}
}
