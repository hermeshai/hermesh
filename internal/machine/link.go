package machine

import (
	"context"
	"errors"
	"fmt"
	"github.com/hermeshai/hermesh/pkg/metrics"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/machine/v1/machinev1connect"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/gen/go/hermesh/session/v1/sessionv1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

type tunnel struct {
	principal *Principal
	conn      net.Conn
}

// runLink keeps the relay connection up with jittered exponential backoff.
func (n *Machine) runLink(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		d := &transport.Dialer{RelayURL: n.relayURL, Role: relayv1.Role_ROLE_MACHINE, Key: n.key, Credentials: n.credentials, HTTPClient: n.cfg.HTTPClient}
		conn, err := d.Dial(ctx)
		n.mu.Lock()
		n.refused = gone(err)
		refused := n.refused
		n.mu.Unlock()
		if refused && n.cfg.SelfErase && n.checkErase(ctx) {
			n.mu.Lock()
			n.erased = true
			n.mu.Unlock()
			return
		}
		if err != nil {
			n.log.Warn("relay connection failed", "err", err, "retry_in", backoff)
		} else {
			start := time.Now()
			n.setConnected(true)
			n.log.Info("connected to relay")
			n.serveConn(ctx, conn)
			n.setConnected(false)
			n.log.Warn("relay connection lost")
			if time.Since(start) > time.Minute {
				backoff = time.Second
			}
		}
		sleep := time.Duration(rand.Int64N(int64(backoff))) + backoff/2
		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func (n *Machine) setConnected(v bool) {
	n.mu.Lock()
	n.connected = v
	n.mu.Unlock()
}

func (n *Machine) serveConn(ctx context.Context, conn *transport.RelayConn) {
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.Close()
	go func() {
		select {
		case <-lctx.Done():
			conn.Close()
		case <-conn.Done():
			cancel()
		}
	}()
	n.mu.Lock()
	n.conn = conn
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		if n.conn == conn {
			n.conn = nil
		}
		n.mu.Unlock()
	}()
	go n.watchUpdates(lctx, conn)
	go n.reportStatus(lctx, conn)
	go func() {
		n.registerInbox(lctx, conn)
		n.followInbox(lctx, conn)
	}()
	for {
		st, hdr, err := conn.AcceptStream()
		if err != nil {
			return
		}
		if hdr.Kind != relayv1.StreamKind_STREAM_KIND_TUNNEL {
			st.Close()
			continue
		}
		go n.serveTunnel(lctx, st)
	}
}

func (n *Machine) control(conn *transport.RelayConn) (relayv1connect.MachineControlServiceClient, error) {
	hc, err := conn.Control()
	if err != nil {
		return nil, err
	}
	return relayv1connect.NewMachineControlServiceClient(hc, "http://relay", transport.ClientNoCompression), nil
}

// reportStatus tells the relay the machine's name and version when it
// connects and again when it is renamed: nothing else about the machine goes
// to the relay (agents and the owner ask it end to end).
func (n *Machine) reportStatus(ctx context.Context, conn *transport.RelayConn) {
	for {
		c, err := n.control(conn)
		if err == nil {
			_, err = c.ReportStatus(ctx, &relayv1.ReportStatusRequest{Name: n.Name(), Version: Version})
		}
		retry := (<-chan time.Time)(nil)
		if err != nil {
			retry = time.After(5 * time.Second)
		}
		select {
		case <-ctx.Done():
			return
		case <-retry:
		case <-n.statusKick:
		}
	}
}

func (n *Machine) watchUpdates(ctx context.Context, conn *transport.RelayConn) {
	c, err := n.control(conn)
	if err != nil {
		return
	}
	stream, err := c.WatchUpdates(ctx, &relayv1.WatchUpdatesRequest{})
	if err != nil {
		return
	}
	defer stream.Close()
	for stream.Receive() {
		n.handleUpdate(stream.Msg())
	}
}

func (n *Machine) handleUpdate(u *relayv1.MachineUpdate) {
	switch v := u.Update.(type) {
	case *relayv1.MachineUpdate_RootChain:
		n.acceptRootChain(v.RootChain)
	case *relayv1.MachineUpdate_Revocations:
		n.applyRevocations(v.Revocations)
	case *relayv1.MachineUpdate_MachineCert:
		n.acceptMachineCert(v.MachineCert)
	case *relayv1.MachineUpdate_PushTargets:
		n.acceptPushTargets(v.PushTargets)
	case *relayv1.MachineUpdate_Grants:
		n.acceptGrants(v.Grants)
	}
}

func (n *Machine) acceptRootChain(rc *identityv1.RootChain) {
	changed, err := n.trust.AcceptRootChain(rc)
	if err != nil {
		n.log.Warn("ignored root chain", "err", err)
		return
	}
	if changed {
		n.st.SetProto(kRootChain, n.trust.RootChain())
		n.audit.Log(AuditRecord{Kind: "root_chain", Detail: fmt.Sprintf("the owner's trusted devices changed (root cert v%d)", n.trust.RootVersion())})
		n.log.Info("adopted a longer owner root chain", "version", n.trust.RootVersion())
	}
}

func (n *Machine) acceptMachineCert(nc *identityv1.Signed) {
	_, pub, err := n.trust.VerifyMachineCert(nc)
	if err != nil || !pub.Equal(n.key.Public()) {
		return
	}
	n.mu.Lock()
	n.machineCert = nc
	n.mu.Unlock()
	n.st.SetProto(kMachineCert, nc)
}

type principalKey struct{}

func principalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

func (n *Machine) serveTunnel(ctx context.Context, raw net.Conn) {
	defer raw.Close()
	hsStart := time.Now()
	tc, sub, err := transport.ServerHandshake(ctx, raw, n.key.CryptoSigner(), n.credentials, n.trust, func(s *identity.Subject) error {
		if s.Kind == identity.SubjectClient {
			return n.checkGrant(s.Grant)
		}
		return nil
	})
	if err != nil {
		n.audit.Log(AuditRecord{Kind: "tunnel", Decision: "deny", Reason: err.Error()})
		return
	}
	defer tc.Close()
	mHandshake.Since(hsStart)
	mTunnels.Add(1)
	defer mTunnels.Add(-1)
	p := &Principal{Kind: PrincipalOwner}
	if sub.Kind == identity.SubjectClient {
		p = &Principal{Kind: PrincipalClient, ClientID: sub.ID, Grant: sub.Grant}
		n.rememberClient(sub.ID, sub.Grant)
	}
	t := &tunnel{principal: p, conn: tc}
	n.mu.Lock()
	n.tunnels[t] = struct{}{}
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.tunnels, t)
		n.mu.Unlock()
	}()
	n.audit.Log(AuditRecord{Kind: "tunnel", Subject: p.ID(), Decision: "allow"})
	transport.ServeH2(context.WithValue(ctx, principalKey{}, p), tc, n.handler())
}

func (n *Machine) handler() http.Handler {
	mux := http.NewServeMux()
	opts := connect.WithHandlerOptions(connect.WithInterceptors(n.authInterceptor()), transport.NoCompression)
	mux.Handle(sessionv1connect.NewSessionServiceHandler(&sessionService{n: n}, opts))
	mux.Handle(machinev1connect.NewMachineServiceHandler(&machineService{n: n}, opts))
	mux.Handle(machinev1connect.NewMachineAdminServiceHandler(&adminService{n: n}, opts))
	return mux
}

var errUnauthenticated = errors.New("unauthenticated")

// authInterceptor re-checks the caller's grant on every request (it may
// have been revoked since the tunnel opened) and keeps admin RPCs to the
// owner.
func (n *Machine) authInterceptor() connect.Interceptor {
	check := func(ctx context.Context, procedure string) error {
		p := principalFrom(ctx)
		if p == nil {
			return connect.NewError(connect.CodeUnauthenticated, errUnauthenticated)
		}
		if p.Kind == PrincipalClient {
			if err := n.checkGrant(p.Grant); err != nil {
				return connect.NewError(connect.CodePermissionDenied, err)
			}
			if len(procedure) > 0 && isAdminProcedure(procedure) {
				return connect.NewError(connect.CodePermissionDenied, errors.New("owner only"))
			}
		}
		return nil
	}
	return &interceptor{check: check}
}

func isAdminProcedure(p string) bool {
	return strings.HasPrefix(p, "/"+machinev1connect.MachineAdminServiceName+"/")
}

type interceptor struct {
	check func(ctx context.Context, procedure string) error
}

func (i *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		start := time.Now()
		if err := i.check(ctx, req.Spec().Procedure); err != nil {
			return nil, err
		}
		res, err := next(ctx, req)
		rpcHistogram(req.Spec().Procedure).Since(start)
		return res, err
	}
}

func (i *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := i.check(ctx, conn.Spec().Procedure); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

var (
	mHandshake = metrics.Default.Histogram("hermesh_machine_handshake_seconds")
	mTunnels   = metrics.Default.Gauge("hermesh_machine_tunnels_active")
	rpcHists   sync.Map
)

func rpcHistogram(procedure string) *metrics.Histogram {
	if h, ok := rpcHists.Load(procedure); ok {
		return h.(*metrics.Histogram)
	}
	method := procedure[strings.LastIndexByte(procedure, '/')+1:]
	h := metrics.Default.Histogram(`hermesh_machine_rpc_seconds{method="` + method + `"}`)
	rpcHists.Store(procedure, h)
	return h
}
