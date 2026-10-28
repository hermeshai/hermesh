package relay

import (
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// An erased owner leaves only its order behind: the relay is unclaimed, its
// owner's records and connection log are gone, and the order and its acks
// are kept for EraseRetention.
func TestEraseOwnerStore(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const owner = "o_x"
	chain := &identityv1.RootChain{Certs: []*identityv1.Signed{{Body: []byte("cert")}}}
	if err := st.SaveClaim(owner, chain, &identityv1.Signed{Body: []byte("device")}); err != nil {
		t.Fatal(err)
	}
	st.LogConnection(owner, 1, "m_1", "10.0.0.1", "connected")
	if err := st.PutMessage(owner, Message{ID: "msg_a", Inbox: "c_1", Sealed: []byte("x"), ReceivedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now()
	order := &identityv1.Signed{Body: []byte("order")}
	if err := st.EraseOwner(owner, "e1", order, chain, t0); err != nil {
		t.Fatal(err)
	}
	if id, _ := st.OwnerID(); id != "" {
		t.Fatalf("still claimed by %q", id)
	}
	if code, err := st.ClaimCode(); err != nil || code == "" {
		t.Fatalf("no new claim code: %q %v", code, err)
	}
	if w, _ := st.WaitingMessages(owner, "c_1"); len(w) != 0 {
		t.Fatalf("messages kept: %v", w)
	}
	var logged int
	st.db.QueryRow(`SELECT COUNT(*) FROM connection_log`).Scan(&logged)
	if logged != 0 {
		t.Fatalf("%d connections still logged", logged)
	}

	st.AckErase("e1", "m_1", t0)
	st.AckErase("e1", "m_1", t0) // again: once
	st.AckErase("e2", "m_2", t0) // no such erase: ignored
	got, gotChain, acks, err := st.EraseOrder(owner, t0.Add(EraseRetention-time.Hour))
	if err != nil || string(got.GetBody()) != "order" || len(gotChain.GetCerts()) != 1 || !slices.Equal(acks, []string{"m_1"}) {
		t.Fatalf("the order: %v %v %v %v", got, gotChain, acks, err)
	}
	for i := range maxEraseAcks + 10 {
		st.AckErase("e1", fmt.Sprintf("m_%d", i+2), t0)
	}
	if _, _, acks, _ := st.EraseOrder(owner, t0); len(acks) != maxEraseAcks {
		t.Fatalf("%d acks kept", len(acks))
	}
	if got, _, _, err := st.EraseOrder(owner, t0.Add(EraseRetention+time.Hour)); got != nil || err != nil {
		t.Fatalf("the order after %v: %v %v", EraseRetention, got, err)
	}
	if got, _, _, _ := st.EraseOrder("o_other", t0); got != nil {
		t.Fatal("an order for an owner that erased nothing")
	}
}
