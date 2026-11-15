package screen

import (
	"net"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/transport/v5"
)

// A restart must not reuse an interface snapshot from the old network.
// Poison only the test's cached flags; the OS interfaces stay untouched.
func TestNetworkRefreshesInterfacesOnRestart(t *testing.T) {
	n, err := newSystemNet()
	if err != nil {
		t.Fatal(err)
	}
	a, err := ice.NewAgentWithOptions(ice.WithNet(n), ice.WithNetworkTypes([]ice.NetworkType{ice.NetworkTypeUDP4}),
		ice.WithCandidateTypes([]ice.CandidateType{ice.CandidateTypeHost}), ice.WithIncludeLoopback())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for run := 0; run < 2; run++ {
		cached, err := n.Net.Interfaces()
		if err != nil {
			t.Fatal(err)
		}
		for _, iface := range cached {
			iface.Flags &^= net.FlagUp
		}
		if run > 0 {
			if err := a.Restart("", ""); err != nil {
				t.Fatal(err)
			}
		}
		done := make(chan struct{})
		if err := a.OnCandidate(func(c ice.Candidate) {
			if c == nil {
				close(done)
			}
		}); err != nil {
			t.Fatal(err)
		}
		if err := a.GatherCandidates(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("gather did not finish")
		}
		candidates, err := a.GetLocalCandidates()
		if err != nil || len(candidates) == 0 {
			t.Fatalf("gather %d reused stale interfaces: candidates=%v, err=%v", run, candidates, err)
		}
	}
}

func TestFilterInterfacesExcludesVirtualAndProxyAddresses(t *testing.T) {
	mkIface := func(name string, cidrs ...string) *transport.Interface {
		ifc := transport.NewInterface(net.Interface{Name: name, Flags: net.FlagUp})
		for _, cidr := range cidrs {
			ip, ipNet, err := net.ParseCIDR(cidr)
			if err != nil {
				t.Fatal(err)
			}
			ipNet.IP = ip
			ifc.AddAddress(ipNet)
		}
		return ifc
	}
	in := []*transport.Interface{
		mkIface("en0", "192.168.1.127/24", "fe80::1/64", "2408:8207:185c:e470::1/64", "fdfe:5bbd:6105::1/64"),
		mkIface("bridge100", "192.168.64.1/24", "fdfe:5bbd:6105:0:bd:c4ff:fe29:7364/64"),
		mkIface("bridge101", "10.211.55.2/24", "fdb2:2c26:f4e4::1/64"),
		mkIface("utun4", "198.18.0.1/15"),
		mkIface("utun5", "100.64.0.2/10"),
		mkIface("pdp_ip0", "10.91.197.177/32", "2408:8409:1b30:33e3::1/64"),
		mkIface("pdp_ip1", "2408:8509:2530:5060::1/64"),
		mkIface("ipsec0", "2408:8509:2550:e79a:1c03:f85d:191a:802/64"),
		mkIface("ipsec1", "2408:8509:2550:e79a:1c03:f85d:191a:802/64"),
		mkIface("utun6", "100.64.0.2/10"),
		mkIface("awdl0", "fe80::2/64"),
	}
	got := filterInterfaces(in)
	names := map[string][]string{}
	for _, iface := range got {
		addrs, err := iface.Addrs()
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range addrs {
			names[iface.Name] = append(names[iface.Name], addrIP(a).String())
		}
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 interfaces (en0, utun5, pdp_ip0), got %v", names)
	}
	if got := names["en0"]; len(got) != 2 || got[0] != "192.168.1.127" || got[1] != "2408:8207:185c:e470::1" {
		t.Fatalf("unexpected en0 addrs: %v", got)
	}
	if got := names["utun5"]; len(got) != 1 || got[0] != "100.64.0.2" {
		t.Fatalf("unexpected utun5 addrs: %v", got)
	}
	if got := names["pdp_ip0"]; len(got) != 2 {
		t.Fatalf("unexpected pdp_ip0 addrs: %v", got)
	}
}

func TestFilterInterfacesDeduplicatesSameV6Prefix(t *testing.T) {
	mkIface := func(name string, cidrs ...string) *transport.Interface {
		ifc := transport.NewInterface(net.Interface{Name: name, Flags: net.FlagUp})
		for _, cidr := range cidrs {
			ip, ipNet, err := net.ParseCIDR(cidr)
			if err != nil {
				t.Fatal(err)
			}
			ipNet.IP = ip
			ifc.AddAddress(ipNet)
		}
		return ifc
	}
	got := filterInterfaces([]*transport.Interface{
		mkIface("en0",
			"192.168.1.127/24",
			"2408:8207:2537:d390:4195:9136:bf3d:d4ec/64",
			"2408:8207:2537:d390:4a6:df13:f223:5dc6/64",
			"2408:8207:2537:d390:c084:4a97:85c2:9238/64",
			"2408:8207:2537:d391::2/64",
		),
	})
	if len(got) != 1 {
		t.Fatalf("expected 1 interface, got %d", len(got))
	}
	addrs, err := got[0].Addrs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"192.168.1.127",
		"2408:8207:2537:d390:4195:9136:bf3d:d4ec",
		"2408:8207:2537:d391::2",
	}
	var ips []string
	for _, a := range addrs {
		ips = append(ips, addrIP(a).String())
	}
	if len(ips) != len(want) {
		t.Fatalf("got %v, want %v", ips, want)
	}
	for i := range want {
		if ips[i] != want[i] {
			t.Fatalf("got %v, want %v", ips, want)
		}
	}
}
