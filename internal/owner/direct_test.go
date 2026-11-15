package owner

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	screenv1 "github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/screen/v1/screenv1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

func TestDirectAddrsPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	dev, err := Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	o := dev.owner()
	o.saveDirectAddrs("m_1", []string{" 192.168.1.2:9000 ", "", "[2408::1]:9000"})
	want := []string{"192.168.1.2:9000", "[2408::1]:9000"}
	if got := o.directAddrs("m_1"); !slices.Equal(got, want) {
		t.Fatalf("directAddrs = %v, want %v", got, want)
	}
	o.noteDirectSuccess("m_1", "[2408::1]:9000")
	wantPref := []string{"[2408::1]:9000", "192.168.1.2:9000"}
	if got := o.directAddrs("m_1"); !slices.Equal(got, wantPref) {
		t.Fatalf("preferred directAddrs = %v, want %v", got, wantPref)
	}
	dev.Close()

	dev2, err := Open(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer dev2.Close()
	o2 := dev2.owner()
	if got := o2.directAddrs("m_1"); !slices.Equal(got, want) {
		t.Fatalf("reloaded directAddrs = %v, want %v", got, want)
	}
	o2.saveDirectAddrs("m_1", nil)
	if got := o2.directAddrs("m_1"); len(got) != 0 {
		t.Fatalf("cleared directAddrs = %v, want empty", got)
	}
}

type stubScreenService struct {
	screenv1connect.UnimplementedScreenServiceHandler
}

func (stubScreenService) Connect(_ context.Context, _ *screenv1.ConnectRequest) (*screenv1.ConnectResponse, error) {
	return &screenv1.ConnectResponse{}, nil
}

func TestScreenCallDirectBeforeRelayConnected(t *testing.T) {
	root, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	device, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	machKey, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerID := identity.OwnerID(identity.MarshalPublicKey(root.Public()))
	machineID := identity.MachineID(identity.MarshalPublicKey(machKey.Public()))
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
	dc, err := identity.Sign(&identityv1.OwnerDeviceCert{
		OwnerId:         ownerID,
		DevicePublicKey: identity.MarshalPublicKey(device.Public()),
		IssuedAt:        timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	mc, err := identity.Sign(&identityv1.MachineCert{
		OwnerId:          ownerID,
		MachineId:        machineID,
		MachinePublicKey: identity.MarshalPublicKey(machKey.Public()),
		IssuedAt:         timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	tr := identity.NewTrust(ownerID)
	if _, err := tr.AcceptRootChain(chain); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mux := http.NewServeMux()
	mux.Handle(screenv1connect.NewScreenServiceHandler(stubScreenService{}))
	machCreds := func() *identityv1.Credentials {
		return &identityv1.Credentials{RootChain: chain, MachineName: "box", Subject: &identityv1.Credentials_MachineCert{MachineCert: mc}}
	}
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tc, _, err := transport.ServerHandshake(t.Context(), c, machKey.CryptoSigner(), machCreds, tr, func(*identity.Subject) error { return nil })
				if err != nil {
					return
				}
				defer tc.Close()
				transport.ServeH2(t.Context(), tc, mux)
			}(raw)
		}
	}()

	slowRelay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-r.Context().Done():
		}
	}))
	defer slowRelay.Close()

	dev, err := Open(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	o := dev.owner()
	o.mu.Lock()
	o.ownerID = ownerID
	o.relayURL = slowRelay.URL
	o.trust = tr
	o.device = device
	o.deviceCert = dc
	o.mu.Unlock()
	o.saveDirectAddrs(machineID, []string{ln.Addr().String()})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	call := screenCall(o, machineID, screenv1connect.ScreenServiceClient.Connect)
	if _, err := call(ctx, &screenv1.ConnectRequest{Offer: "sdp"}); err != nil {
		t.Fatalf("direct screenCall without relay failed: %v", err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("direct screenCall took %v, expected far faster than 1.5s blocked relay", took)
	}
	// Second call reuses cached direct machine without dialing again.
	if cached := o.cachedDirectMachine(machineID); cached == nil {
		t.Fatal("expected cached direct machine after first call")
	}
	o.closeDirectMachines()
}
