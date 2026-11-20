package machine

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	machinev1 "github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1/machinev1connect"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/session/v1/sessionv1connect"
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

func TestServeDirectOwnerOnlyAndSignedAnswerPolicy(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	n := h.n
	id := h.start("ask Branch|Which fictional branch?|Main,Dev")
	q := h.wait(id, waiting).GetProgress().GetPendingQuestion()
	if q.GetLocalAnswerAvailable() {
		t.Fatal("an opaque adapter was advertised as local")
	}

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
		RootKeys: []*identityv1.RootKey{{PublicKey: identity.MarshalPublicKey(root.Public()), Platform: "ios"}},
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	desktopRoot, err := identity.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	paired, err := identity.Sign(&identityv1.RootCert{OwnerId: ownerID, Version: 2, IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{
			{PublicKey: identity.MarshalPublicKey(root.Public()), Platform: "ios"},
			{PublicKey: identity.MarshalPublicKey(desktopRoot.Public()), Platform: "macos"},
		}}, root)
	if err != nil {
		t.Fatal(err)
	}
	chain := &identityv1.RootChain{Certs: []*identityv1.Signed{rc, paired}}
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

	// Use the old RPC as an earlier iPhone would, and also exercise the one
	// new iPhones require. Keep the question pending to test both tunnels.
	checkPhone := func(session sessionv1connect.SessionServiceClient) error {
		answer := &sessionv1.AnswerQuestionRequest{SessionId: id, QuestionId: q.GetQuestionId(),
			Answers: []*sessionv1.Answer{{Text: "fictional blocked answer"}}}
		for _, call := range []func(context.Context, *sessionv1.AnswerQuestionRequest) (*sessionv1.Session, error){
			session.AnswerQuestion, session.AnswerLocalQuestion,
		} {
			if _, err := call(ctx, answer); connect.CodeOf(err) != connect.CodeFailedPrecondition {
				return fmt.Errorf("opaque agent answer: %v", err)
			}
		}
		got, err := session.GetSession(ctx, &sessionv1.GetSessionRequest{SessionId: id})
		if err != nil || got.GetProgress().GetPendingQuestion().GetQuestionId() != q.GetQuestionId() {
			return fmt.Errorf("question changed after refusal: %v %v", got, err)
		}
		return nil
	}

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
		if err != nil {
			return err
		}
		return checkPhone(sessionv1connect.NewSessionServiceClient(h2, "https://machine"))
	}

	// Owner device succeeds E2E handshake + GetInfo.
	if err := dialWith(device, &identityv1.Credentials{RootChain: chain, Subject: &identityv1.Credentials_DeviceCert{DeviceCert: dc}}); err != nil {
		t.Fatalf("owner direct connect failed: %v", err)
	}

	// Agent client grant must be denied on direct door even if grant covers "*".
	if err := dialWith(clientKey, &identityv1.Credentials{RootChain: chain, Subject: &identityv1.Credentials_Grant{Grant: grant}}); err == nil {
		t.Fatal("agent grant unexpectedly succeeded on direct door")
	}

	// The same signed identity and write checks apply through real QUIC.
	listener, err := transport.ListenQUIC("127.0.0.1:0", n.key.CryptoSigner(), n.credentials, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept(ctx)
			if err != nil {
				return
			}
			go n.serveQUICTunnel(ctx, conn, "test QUIC", true)
		}
	}()
	dialQUIC := func(cert *identityv1.Signed) sessionv1connect.SessionServiceClient {
		t.Helper()
		h3, _, err := transport.DialQUIC(ctx, listener.Addr().String(), device.CryptoSigner(),
			func() *identityv1.Credentials {
				return &identityv1.Credentials{RootChain: chain, Subject: &identityv1.Credentials_DeviceCert{DeviceCert: cert}}
			}, tr, n.machineID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { h3.Close() })
		return sessionv1connect.NewSessionServiceClient(h3, "https://machine")
	}
	phone := dialQUIC(dc)
	if err := checkPhone(phone); err != nil {
		t.Fatalf("iPhone over QUIC: %v", err)
	}
	if _, err := phone.AnswerQuestion(ctx, &sessionv1.AnswerQuestionRequest{SessionId: id, QuestionId: q.GetQuestionId(), Decline: true}); err != nil {
		t.Fatalf("fixed decline over QUIC: %v", err)
	}

	desktopCert, err := identity.Sign(&identityv1.OwnerDeviceCert{
		OwnerId: ownerID, DevicePublicKey: identity.MarshalPublicKey(device.Public()), IssuedAt: timestamppb.Now(),
	}, desktopRoot)
	if err != nil {
		t.Fatal(err)
	}
	id = h.start("ask Branch|Which fictional branch?|Main,Dev")
	q = h.wait(id, waiting).GetProgress().GetPendingQuestion()
	desktop := dialQUIC(desktopCert)
	if _, err := desktop.AnswerQuestion(ctx, &sessionv1.AnswerQuestionRequest{SessionId: id, QuestionId: q.GetQuestionId(),
		Answers: []*sessionv1.Answer{{Selected: []string{"Dev"}}}}); err != nil {
		t.Fatalf("verified desktop over QUIC: %v", err)
	}
	h.said(id, "answer: Dev")
}
