package owner

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/peer"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/relay"
)

// Real-UDP QUIC/HTTP3 end-to-end test:
//  1. Real Machine starts serveDirect + serveDirectQUIC and advertises direct_quic_addrs.
//  2. Owner dials Machine directly via QUIC/HTTP3 (racing QUIC ahead of TCP), wins on QUIC
//     (Path() == "quic_lan" or "quic_ipv6"), and executes concurrent Connect GetInfo RPCs.
//  3. Cellular-only mode strips RFC1918 quic:// addresses while preserving IPv6 quic:// addresses.
func TestDirectQUICEndToEndConnectRPCAndCellularFilter(t *testing.T) {
	dir := t.TempDir()
	st, err := relay.OpenStore(filepath.Join(dir, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	root, _ := identity.GenerateKey()
	rootPub := identity.MarshalPublicKey(root.Public())
	ownerID := identity.OwnerID(rootPub)
	rc, err := identity.Sign(&identityv1.RootCert{
		OwnerId:  ownerID,
		Version:  1,
		IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{{PublicKey: rootPub, Label: "root"}},
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	chain := &identityv1.RootChain{Certs: []*identityv1.Signed{rc}}
	dev, _ := identity.GenerateKey()
	devCert, err := identity.Sign(&identityv1.OwnerDeviceCert{
		OwnerId:         ownerID,
		DevicePublicKey: identity.MarshalPublicKey(dev.Public()),
		Label:           "iPhone",
		IssuedAt:        timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveClaim(ownerID, chain, devCert); err != nil {
		t.Fatal(err)
	}

	mach, err := machine.Open(machine.Config{DataDir: filepath.Join(dir, "machine"), WorkspaceMode: "none"})
	if err != nil {
		t.Fatal(err)
	}
	machID := mach.MachineID()
	machCert, err := identity.Sign(&identityv1.MachineCert{
		OwnerId:          ownerID,
		MachineId:        machID,
		MachinePublicKey: mach.PublicKeySPKI(),
		IssuedAt:         timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutMachineCert(ownerID, machID, machCert); err != nil {
		t.Fatal(err)
	}

	rs, err := relay.New(relay.Config{Backend: st})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rs.Close() })
	srv := httptest.NewServer(rs.Handler())
	t.Cleanup(srv.Close)

	if err := mach.Install(ownerID, srv.URL, chain, machCert); err != nil {
		t.Fatal(err)
	}
	if err := mach.SetName("quic-mac"); err != nil {
		t.Fatal(err)
	}
	machCtx, machCancel := context.WithCancel(context.Background())
	machDone := make(chan struct{})
	go func() {
		defer close(machDone)
		_ = mach.Run(machCtx)
	}()
	t.Cleanup(func() {
		machCancel()
		<-machDone
		mach.Close()
	})

	// Wait until machine starts listening on QUIC and advertises direct_quic_addrs.
	var quicAddrs, tcpAddrs []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ms := mach.Status()
		if len(ms.DirectQuicAddrs) > 0 && len(ms.DirectAddrs) > 0 {
			quicAddrs = ms.DirectQuicAddrs
			tcpAddrs = ms.DirectAddrs
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(quicAddrs) == 0 {
		t.Skip("no non-loopback network interface available for direct QUIC test")
	}

	// Verify cellular-only strips RFC1918 quic:// addresses while preserving IPv6 quic://.
	d, err := Open(Config{DataDir: filepath.Join(dir, "owner"), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	o := d.owner()
	combined := combineDirectAddrs([]string{"192.168.1.5:4000", "[2408::1]:4000"}, []string{"192.168.1.5:4001", "[2408::1]:4001"})
	o.saveDirectAddrs(machID, combined)
	o.SetCellularOnly(true)
	wantCell := []string{"quic://[2408::1]:4000", "[2408::1]:4001"}
	if got := o.directAddrs(machID); !slices.Equal(got, wantCell) {
		t.Fatalf("cellular directAddrs = %v, want %v", got, wantCell)
	}
	o.SetCellularOnly(false)

	// Dial Machine directly over QUIC/HTTP3 via peer.Dialer.DialDirect.
	trust := identity.NewTrust(ownerID)
	if _, err := trust.AcceptRootChain(chain); err != nil {
		t.Fatal(err)
	}
	dialer := &peer.Dialer{
		RelayURL: srv.URL,
		Role:     relayv1.Role_ROLE_OWNER,
		Key:      dev,
		Trust:    trust,
		Credentials: func() *identityv1.Credentials {
			return &identityv1.Credentials{RootChain: chain, Subject: &identityv1.Credentials_DeviceCert{DeviceCert: devCert}}
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Put QUIC ahead of TCP (just as combineDirectAddrs does) and verify QUIC wins!
	m, err := dialer.DialDirect(ctx, machID, combineDirectAddrs(quicAddrs, tcpAddrs))
	if err != nil {
		t.Fatalf("DialDirect over QUIC: %v", err)
	}
	defer m.Close()
	if m.Path() != "quic_lan" && m.Path() != "quic_ipv6" {
		t.Fatalf("expected QUIC path, got %q", m.Path())
	}

	// Run 12 concurrent Connect GetInfo RPCs over independent HTTP/3 streams.
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := m.Info.GetInfo(ctx, &machinev1.GetInfoRequest{})
			if err != nil {
				errs <- err
				return
			}
			if res.GetStatus().GetName() != "quic-mac" {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Connect RPC over QUIC/H3 failed: %v", err)
	}

	// Verify ProbeOrForgetTunnels keeps a healthy QUIC tunnel alive (no spurious
	// local close on transient mobile path events) and drops it once the peer stops.
	client := dialer.Detached()
	defer client.Close()
	if !client.AdoptDirect(machID, m) {
		t.Fatal("AdoptDirect rejected live QUIC tunnel")
	}
	client.ProbeOrForgetTunnels(ctx)
	if !m.Alive() {
		t.Fatal("ProbeOrForgetTunnels unexpectedly closed a healthy QUIC tunnel")
	}
	if path, _, ok := client.MachineTunnel(machID); !ok || (path != "quic_lan" && path != "quic_ipv6") {
		t.Fatalf("healthy QUIC tunnel evicted after ProbeOrForgetTunnels: ok=%v path=%q", ok, path)
	}

	// Verify directFastMachine singleflight: 8 concurrent callers while relay is
	// pending must share one QUIC tunnel without closing each other's streams.
	o.mu.Lock()
	o.ownerID = ownerID
	o.relayURL = srv.URL
	o.device = dev
	o.deviceCert = devCert
	o.trust = trust
	o.mu.Unlock()
	o.saveDirectAddrs(machID, combineDirectAddrs(quicAddrs, tcpAddrs))
	const workers = 8
	gotMachines := make([]*peer.Machine, workers)
	sfErrs := make(chan error, workers)
	start := make(chan struct{})
	var sfwg sync.WaitGroup
	for i := range workers {
		sfwg.Add(1)
		go func(idx int) {
			defer sfwg.Done()
			<-start
			dn := o.directFastMachine(ctx, machID)
			gotMachines[idx] = dn
			if dn == nil {
				return
			}
			if _, err := dn.Info.GetInfo(ctx, &machinev1.GetInfoRequest{}); err != nil {
				sfErrs <- err
			}
		}(i)
	}
	close(start)
	sfwg.Wait()
	close(sfErrs)
	for err := range sfErrs {
		t.Fatalf("concurrent directFastMachine RPC failed: %v", err)
	}
	first := gotMachines[0]
	if first == nil || !first.Alive() {
		t.Fatalf("directFastMachine returned nil or dead tunnel: %v", first)
	}
	for i := 1; i < workers; i++ {
		if gotMachines[i] != first {
			t.Fatalf("directFastMachine did not coalesce concurrent dials: worker %d got %p, want %p", i, gotMachines[i], first)
		}
	}
	o.closeDirectMachines()

	// Verify 5G -> Wi-Fi upgrade: start with a strictly relayed tunnel on a live
	// peer.Client, verify PunchDirect returns live QUIC endpoints, then call
	// UpgradeToDirect(ctx, true) and confirm seamless atomic switch from "relay"
	// to direct QUIC ("quic_lan"/"quic_ipv6") with old relay tunnel closed.
	relayDialer := &peer.Dialer{
		RelayURL: srv.URL,
		Role:     relayv1.Role_ROLE_OWNER,
		Key:      dev,
		Trust:    trust,
		Credentials: func() *identityv1.Credentials {
			return &identityv1.Credentials{RootChain: chain, Subject: &identityv1.Credentials_DeviceCert{DeviceCert: devCert}}
		},
		Direct: func(id string) []string {
			if id == machID {
				return combineDirectAddrs(quicAddrs, tcpAddrs)
			}
			return nil
		},
	}
	relayClient, err := relayDialer.Connect(ctx)
	if err != nil {
		t.Fatalf("relay Connect: %v", err)
	}
	defer relayClient.Close()
	relayed, err := relayClient.RelayMachine(ctx, machID)
	if err != nil {
		t.Fatalf("RelayMachine: %v", err)
	}
	if relayed.Path() != "relay" {
		t.Fatalf("expected relay path before upgrade, got %q", relayed.Path())
	}
	punchRes, err := relayed.Info.PunchDirect(ctx, &machinev1.PunchDirectRequest{})
	if err != nil {
		t.Fatalf("PunchDirect RPC failed: %v", err)
	}
	if len(punchRes.GetDirectQuicAddrs()) == 0 {
		t.Fatal("PunchDirect returned empty direct_quic_addrs")
	}
	upgraded := relayClient.UpgradeToDirect(ctx, true)
	if !slices.Contains(upgraded, machID) {
		// Auto-punch goroutine may have already upgraded it onto quic_ipv6; verify
		// tunnel is direct either way.
		if path, _, ok := relayClient.MachineTunnel(machID); !ok || path == "relay" {
			t.Fatalf("UpgradeToDirect did not upgrade relayed machine %s (upgraded=%v path=%q ok=%v)", machID, upgraded, path, ok)
		}
	}
	upPath, _, upAlive := relayClient.MachineTunnel(machID)
	if !upAlive || (upPath != "quic_lan" && upPath != "quic_ipv6") {
		t.Fatalf("after UpgradeToDirect: alive=%v path=%q, want quic_lan or quic_ipv6", upAlive, upPath)
	}
	if relayed.Alive() {
		t.Fatal("old relayed tunnel was not closed after UpgradeToDirect")
	}
	upMach, err := relayClient.Machine(ctx, machID)
	if err != nil {
		t.Fatalf("Machine after UpgradeToDirect: %v", err)
	}
	if res, err := upMach.Info.GetInfo(ctx, &machinev1.GetInfoRequest{}); err != nil || res.GetStatus().GetName() != "quic-mac" {
		t.Fatalf("GetInfo over upgraded QUIC tunnel failed: res=%v err=%v", res, err)
	}

	// Probing a live tunnel (as replica does when relay LastSeen advances while
	// online) must keep the exact same tunnel open without interrupting it.
	relayClient.ProbeOrForgetMachine(ctx, machID)
	if !upMach.Alive() {
		t.Fatal("ProbeOrForgetMachine closed a healthy live tunnel")
	}
	if got, err := relayClient.Machine(ctx, machID); err != nil || got != upMach {
		t.Fatalf("Machine after healthy probe returned %p (err=%v), want same live tunnel %p", got, err, upMach)
	}

	machCancel()
	<-machDone
	relayClient.ProbeOrForgetMachine(ctx, machID)
	if _, _, ok := relayClient.MachineTunnel(machID); ok {
		t.Fatal("dead QUIC tunnel was not evicted by ProbeOrForgetMachine")
	}
	client.ProbeOrForgetTunnels(ctx)
	if _, _, ok := client.MachineTunnel(machID); ok {
		t.Fatal("dead QUIC tunnel was not evicted by ProbeOrForgetTunnels")
	}
}

func TestMachinesListedEvictsTunnelOnOfflineOrReconnect(t *testing.T) {
	d, err := Open(Config{DataDir: t.TempDir(), NoLocalMachine: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	o := d.owner()
	r := o.rep

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &sources{
		ctx:     ctx,
		cancel:  cancel,
		settled: make(chan struct{}),
		listed:  make(chan struct{}),
		feeds:   map[string]*feed{},
	}
	// Pre-mark running so machinesListedLocked does not spawn follow goroutines.
	t1 := time.Now().Add(-10 * time.Second).UTC().Truncate(time.Millisecond)
	t2 := t1.Add(5 * time.Second)
	s.feeds["m_remote"] = &feed{online: true, running: true, lastSeen: t1}

	// Same LastSeen while online: neither eviction nor probe.
	s.machines.list = []*localv1.Machine{{MachineId: "m_remote", MachineName: "vps", Online: true, LastSeen: timestamppb.New(t1)}}
	s.machines.err = nil
	r.mu.Lock()
	evict, probe := r.machinesListedLocked(s)
	r.mu.Unlock()
	if len(evict) != 0 || len(probe) != 0 {
		t.Fatalf("unexpected eviction/probe with unchanged LastSeen: evict=%v probe=%v", evict, probe)
	}

	// Non-authoritative error listing (relay unreachable): must NOT evict or probe.
	s.machines.list = []*localv1.Machine{{MachineId: "m_remote", MachineName: "vps", Online: false, LastSeen: timestamppb.New(t1)}}
	s.machines.err = context.DeadlineExceeded
	r.mu.Lock()
	evict, probe = r.machinesListedLocked(s)
	r.mu.Unlock()
	if len(evict) != 0 || len(probe) != 0 {
		t.Fatalf("unexpected eviction/probe on non-authoritative listing: evict=%v probe=%v", evict, probe)
	}

	// Authoritative LastSeen advance while Online stayed true: must schedule a
	// liveness probe instead of blindly killing a potentially healthy tunnel.
	s.machines.list = []*localv1.Machine{{MachineId: "m_remote", MachineName: "vps", Online: true, LastSeen: timestamppb.New(t2)}}
	s.machines.err = nil
	r.mu.Lock()
	evict, probe = r.machinesListedLocked(s)
	r.mu.Unlock()
	if len(evict) != 0 || !slices.Equal(probe, []string{"m_remote"}) {
		t.Fatalf("expected probe (no blind evict) on LastSeen advance while online, got evict=%v probe=%v", evict, probe)
	}

	// Authoritative offline transition: must evict immediately.
	s.machines.list = []*localv1.Machine{{MachineId: "m_remote", MachineName: "vps", Online: false, LastSeen: timestamppb.New(t2)}}
	s.machines.err = nil
	r.mu.Lock()
	evict, probe = r.machinesListedLocked(s)
	r.mu.Unlock()
	if !slices.Equal(evict, []string{"m_remote"}) || len(probe) != 0 {
		t.Fatalf("expected eviction on offline transition, got evict=%v probe=%v", evict, probe)
	}
}
