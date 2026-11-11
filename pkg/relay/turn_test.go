package relay

import (
	"bytes"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pion/turn/v5"
)

// allocate asks the TURN server at addr for a relayed address with a
// credential; it returns the relayed connection.
func allocate(t *testing.T, addr, user, pass string) (net.PacketConn, error) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c, err := turn.NewClient(&turn.ClientConfig{TURNServerAddr: addr, STUNServerAddr: addr, Username: user, Password: pass, Realm: "hermesh",
		Conn: conn, RTO: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := c.Listen(); err != nil {
		t.Fatal(err)
	}
	relayed, err := c.Allocate()
	if err == nil {
		t.Cleanup(func() { relayed.Close() })
	}
	return relayed, err
}

// The nodes of a center: one runs the server, with its forwarded sessions
// in a range of ports; the other only hands out credentials, which the
// first takes, as they share the secret. A credential signed with another
// secret is refused. Packets go both ways through two relayed addresses.
func TestTURNOnAnotherNode(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	// A port for the server, which the other node names.
	l, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	port := l.LocalAddr().(*net.UDPAddr).Port
	l.Close()
	addr := net.JoinHostPort("127.0.0.1", itoa(port))
	serving, err := startTURN(TURNConfig{Listen: addr, Host: "127.0.0.1", Secret: secret, MinPort: 49160, MaxPort: 49200})
	if err != nil {
		t.Fatal(err)
	}
	defer serving.close()
	other, err := startTURN(TURNConfig{Host: addr, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	if other.srv != nil {
		t.Fatal("the node that hands out credentials runs a server too")
	}

	creds := other.servers("m_machine")
	if len(creds) != 2 || creds[1].Username == "" || !strings.Contains(creds[1].Urls[0], addr) {
		t.Fatalf("servers: %v", creds)
	}
	a, err := allocate(t, addr, creds[1].Username, creds[1].Credential)
	if err != nil {
		t.Fatalf("a credential from the other node: %v", err)
	}
	b, err := allocate(t, addr, creds[1].Username, creds[1].Credential)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []net.PacketConn{a, b} {
		if p := r.LocalAddr().(*net.UDPAddr).Port; p < 49160 || p > 49200 {
			t.Fatalf("relayed on port %d, outside 49160-49200", p)
		}
	}
	// Each lets the other's packets in (a permission comes with the first
	// packet sent to a peer), then they talk.
	a.WriteTo([]byte("hi"), b.LocalAddr())
	b.WriteTo([]byte("hi"), a.LocalAddr())
	got := make([]byte, 16)
	for _, x := range []struct{ from, to net.PacketConn }{{a, b}, {b, a}} {
		x.to.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			x.from.WriteTo([]byte("through"), x.to.LocalAddr())
			n, _, err := x.to.ReadFrom(got)
			if err != nil {
				t.Fatalf("nothing came through: %v", err)
			}
			if string(got[:n]) == "through" {
				break
			}
		}
	}

	// Another center's credential: refused.
	stranger, _ := startTURN(TURNConfig{Host: addr, Secret: bytes.Repeat([]byte{8}, 32)})
	bad := stranger.servers("m_machine")
	if _, err := allocate(t, addr, bad[1].Username, bad[1].Credential); err == nil {
		t.Fatal("a credential signed with another secret was taken")
	}
}

// What a node that serves no TURN needs: the server's port, and the secret.
func TestTURNConfigRefused(t *testing.T) {
	for _, c := range []TURNConfig{
		{Host: "turn.example.com", Secret: []byte{1}},
		{Host: "turn.example.com:3478"},
		{Listen: "127.0.0.1:0", Host: "127.0.0.1", MinPort: 49160},
		{Listen: "127.0.0.1:0", Host: "127.0.0.1", MinPort: 49200, MaxPort: 49160},
	} {
		if s, err := startTURN(c); err == nil {
			s.close()
			t.Errorf("%+v was taken", c)
		}
	}
	if lo, hi, err := ParsePortRange("49160-49200"); err != nil || lo != 49160 || hi != 49200 {
		t.Fatalf("ParsePortRange: %d %d %v", lo, hi, err)
	}
	for _, s := range []string{"49160", "49200-49160", "0-10", "a-b", "49160-70000"} {
		if _, _, err := ParsePortRange(s); err == nil {
			t.Errorf("ParsePortRange(%q) was taken", s)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
