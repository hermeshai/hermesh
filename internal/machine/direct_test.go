package machine

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1/machinev1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

func TestStatusForHidesDirectAddrs(t *testing.T) {
	n := openHerMachine(t, t.TempDir(), "")
	defer n.Close()
	n.directPort.Store(45678)

	ownerSt := n.statusFor(&Principal{Kind: PrincipalOwner})
	for _, hp := range ownerSt.DirectAddrs {
		if _, port, err := net.SplitHostPort(hp); err != nil || port != "45678" {
			t.Fatalf("unexpected direct addr %q: %v", hp, err)
		}
	}
	for _, p := range []*Principal{nil, {Kind: PrincipalClient, ClientID: "c1"}} {
		if got := n.statusFor(p).DirectAddrs; len(got) != 0 {
			t.Fatalf("principal %#v saw direct_addrs = %v, want empty", p, got)
		}
	}
}

func TestServeDirectOwnerOnlyAndH2(t *testing.T) {
	n := openHerMachine(t, t.TempDir(), "")
	defer n.Close()

	root, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	device, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerID := identity.OwnerID(identity.MarshalPublicKey(root.Public()))
	rc, err := identity.Sign(&identityv1.RootCert{
		OwnerId:  ownerID,
		Version:  1,
		IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{{PublicKey: identity.MarshalPublicKey(root.Public())}},
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	chain := &identityv1.RootChain{Certs: []*identityv1.Signed{rc}}
	tr := identity.NewTrust(ownerID)
	if _, err := tr.AcceptRootChain(chain); err != nil {
		t.Fatal(err)
	}
	mc, err := identity.Sign(&identityv1.MachineCert{
		OwnerId:          ownerID,
		MachineId:        n.machineID,
		MachinePublicKey: n.PublicKeySPKI(),
		IssuedAt:         timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := identity.Sign(&identityv1.OwnerDeviceCert{
		OwnerId:         ownerID,
		DevicePublicKey: identity.MarshalPublicKey(device.Public()),
		IssuedAt:        timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	clientSPKI := identity.MarshalPublicKey(clientKey.Public())
	grant, err := identity.Sign(&identityv1.Grant{
		OwnerId:         ownerID,
		GrantId:         "g1",
		Version:         1,
		ClientId:        identity.ClientID(clientSPKI),
		ClientPublicKey: clientSPKI,
		MachineIds:      []string{"*"},
		ProjectIds:      []string{"*"},
		Agents:          []string{"claude"},
		NotBefore:       timestamppb.Now(),
		ExpiresAt:       timestamppb.New(time.Now().Add(time.Hour)),
	}, root)
	if err != nil {
		t.Fatal(err)
	}

	n.mu.Lock()
	n.ownerID = ownerID
	n.trust = tr
	n.machineCert = mc
	n.machineName = "test-mac"
	n.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go n.serveDirect(ctx)

	deadline := time.Now().Add(3 * time.Second)
	var port int32
	for time.Now().Before(deadline) {
		if port = n.directPort.Load(); port > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if port <= 0 {
		t.Fatal("direct endpoint did not bind a port")
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	dialWith := func(k *identity.SoftwareKey, creds *identityv1.Credentials) error {
		raw, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		defer raw.Close()
		clientTrust := identity.NewTrust(ownerID)
		if _, err := clientTrust.AcceptRootChain(chain); err != nil {
			return err
		}
		tc, _, err := transport.ClientHandshake(ctx, raw, k.CryptoSigner(), func() *identityv1.Credentials { return creds }, clientTrust, n.machineID)
		if err != nil {
			return err
		}
		defer tc.Close()
		h2, err := transport.NewTunnelClient(tc)
		if err != nil {
			return err
		}
		defer h2.Close()
		_, err = machinev1connect.NewMachineServiceClient(h2, "https://machine").GetInfo(ctx, &machinev1.GetInfoRequest{})
		return err
	}

	// Owner device succeeds E2E handshake + GetInfo.
	if err := dialWith(device, &identityv1.Credentials{RootChain: chain, Subject: &identityv1.Credentials_DeviceCert{DeviceCert: dc}}); err != nil {
		t.Fatalf("owner direct connect failed: %v", err)
	}

	// Agent client grant must be denied on direct door even if grant covers "*".
	if err := dialWith(clientKey, &identityv1.Credentials{RootChain: chain, Subject: &identityv1.Credentials_Grant{Grant: grant}}); err == nil {
		t.Fatal("agent grant unexpectedly succeeded on direct door")
	}
}
