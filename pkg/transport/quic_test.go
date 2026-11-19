package transport

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

func TestQUICE2EMTLSAndConcurrentHTTP3Streams(t *testing.T) {
	root, _ := identity.GenerateKey()
	rootPub := identity.MarshalPublicKey(root.Public())
	ownerID := identity.OwnerID(rootPub)
	rc, err := identity.Sign(&identityv1.RootCert{
		OwnerId:  ownerID,
		Version:  1,
		IssuedAt: timestamppb.Now(),
		RootKeys: []*identityv1.RootKey{{PublicKey: rootPub}},
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	chain := &identityv1.RootChain{Certs: []*identityv1.Signed{rc}}
	trust := identity.NewTrust(ownerID)
	if _, err := trust.AcceptRootChain(chain); err != nil {
		t.Fatal(err)
	}

	machKey, _ := identity.GenerateKey()
	machSPKI := identity.MarshalPublicKey(machKey.Public())
	machID := identity.MachineID(machSPKI)
	machCert, err := identity.Sign(&identityv1.MachineCert{
		OwnerId:          ownerID,
		MachineId:        machID,
		MachinePublicKey: machSPKI,
		IssuedAt:         timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	devKey, _ := identity.GenerateKey()
	devCert, err := identity.Sign(&identityv1.OwnerDeviceCert{
		OwnerId:         ownerID,
		DevicePublicKey: identity.MarshalPublicKey(devKey.Public()),
		IssuedAt:        timestamppb.Now(),
	}, root)
	if err != nil {
		t.Fatal(err)
	}

	machCreds := func() *identityv1.Credentials {
		return &identityv1.Credentials{
			RootChain:   chain,
			MachineName: "quic-mac",
			Subject:     &identityv1.Credentials_MachineCert{MachineCert: machCert},
		}
	}
	devCreds := func() *identityv1.Credentials {
		return &identityv1.Credentials{
			RootChain: chain,
			Subject:   &identityv1.Credentials_DeviceCert{DeviceCert: devCert},
		}
	}

	ln, err := ListenQUIC("127.0.0.1:0", machKey.CryptoSigner(), machCreds, trust, func(s *identity.Subject) error {
		if s.Kind != identity.SubjectOwnerDevice {
			return io.ErrUnexpectedEOF
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		for {
			qconn, err := ln.Accept(ctx)
			if err != nil {
				return
			}
			go func() {
				sub, err := VerifiedQUICSubject(trust, qconn)
				if err != nil {
					_ = qconn.CloseWithError(1, err.Error())
					return
				}
				ServeH3(ctx, qconn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("X-Subject", sub.ID)
					_, _ = w.Write([]byte("pong:" + r.URL.Path))
				}))
			}()
		}
	}()

	dctx, dcancel := context.WithTimeout(ctx, 5*time.Second)
	defer dcancel()
	h3, sub, err := DialQUIC(dctx, ln.Addr().String(), devKey.CryptoSigner(), devCreds, trust, machID)
	if err != nil {
		t.Fatalf("DialQUIC: %v", err)
	}
	defer h3.Close()
	if sub.MachineName != "quic-mac" || !h3.Alive() {
		t.Fatalf("unexpected sub=%+v alive=%v", sub, h3.Alive())
	}

	// Verify 16 concurrent independent HTTP/3 streams with http:// URL normalization.
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, _ := http.NewRequestWithContext(dctx, http.MethodGet, "http://peer/ping", nil)
			resp, err := h3.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				errs <- err
				return
			}
			if string(body) != "pong:/ping" {
				errs <- io.ErrUnexpectedEOF
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent H3 stream error: %v", err)
	}

	// Wrong expected machine ID must fail mTLS verification.
	if bad, _, err := DialQUIC(dctx, ln.Addr().String(), devKey.CryptoSigner(), devCreds, trust, "m_wrong"); err == nil {
		bad.Close()
		t.Fatal("expected mismatching machineID to fail QUIC mTLS verification")
	}

	// Closing marks Alive() false.
	_ = h3.Close()
	if h3.Alive() {
		t.Fatal("expected Alive() == false after Close()")
	}
}
