package screen

import (
	"bytes"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/turn/v5"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

type birthdayWebRTCWrite struct {
	conn       transport.UDPConn
	pool       bool
	controlled ice.AttrControlled
	priority   ice.PriorityAttr
	hasRole    bool
}

type birthdayWebRTCMapping struct {
	phone, target *net.UDPAddr
	local         string
}

// This recorder observes virtual sockets only. Correlating a pool opening's
// transaction ID with vnet's translated packet gives the fixture one known
// mapping; production retains its random probe strategy unchanged.
type birthdayWebRTCWire struct {
	mu        sync.Mutex
	writes    map[[stun.TransactionIDSize]byte]birthdayWebRTCWrite
	macBases  map[string]birthdayWebRTCWrite
	mapping   chan birthdayWebRTCMapping
	allowed   string
	poolStart atomic.Bool
	adopted   atomic.Bool
}

type birthdayWebRTCNet struct {
	transport.Net
	wire  *birthdayWebRTCWire
	phone bool
}

type birthdayWebRTCConn struct {
	transport.UDPConn
	net  *birthdayWebRTCNet
	pool bool
}

func (n *birthdayWebRTCNet) ListenUDP(network string, address *net.UDPAddr) (transport.UDPConn, error) {
	c, err := n.Net.ListenUDP(network, address)
	if err != nil {
		return nil, err
	}
	return &birthdayWebRTCConn{UDPConn: c, net: n, pool: n.phone && n.wire.poolStart.Load()}, nil
}

func (c *birthdayWebRTCConn) record(data []byte) {
	m := &stun.Message{Raw: data}
	if m.Decode() != nil || m.Type != stun.BindingRequest {
		return
	}
	w := birthdayWebRTCWrite{conn: c, pool: c.pool}
	w.hasRole = w.controlled.GetFrom(m) == nil
	_ = w.priority.GetFrom(m)
	c.net.wire.mu.Lock()
	c.net.wire.writes[m.TransactionID] = w
	c.net.wire.mu.Unlock()
}

func (c *birthdayWebRTCConn) WriteTo(data []byte, address net.Addr) (int, error) {
	c.record(data)
	return c.UDPConn.WriteTo(data, address)
}

func (c *birthdayWebRTCConn) WriteToUDP(data []byte, address *net.UDPAddr) (int, error) {
	c.record(data)
	return c.UDPConn.WriteToUDP(data, address)
}

func birthdayWebRTCCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func birthdayWebRTCEventually(t *testing.T, condition func() bool, limit time.Duration, message string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

func birthdayWebRTCNets(t *testing.T, wire *birthdayWebRTCWire) (transport.Net, transport.Net) {
	t.Helper()
	logger := logging.NewDefaultLoggerFactory()
	wan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "0.0.0.0/0", LoggerFactory: logger})
	birthdayWebRTCCheck(t, err)
	makeNet := func(private, subnet, public string, mapping vnet.EndpointDependencyType) *vnet.Net {
		router, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR: subnet, StaticIPs: []string{public}, LoggerFactory: logger,
			NATType: &vnet.NATType{MappingBehavior: mapping, FilteringBehavior: vnet.EndpointAddrPortDependent},
		})
		birthdayWebRTCCheck(t, err)
		n, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{private}})
		birthdayWebRTCCheck(t, err)
		birthdayWebRTCCheck(t, router.AddNet(n))
		birthdayWebRTCCheck(t, wan.AddRouter(router))
		return n
	}
	mac := makeNet("192.168.0.2", "192.168.0.0/24", "27.1.1.1", vnet.EndpointIndependent)
	phone := makeNet("10.2.0.2", "10.2.0.0/24", "28.1.1.1", vnet.EndpointAddrPortDependent)
	serverNet, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"1.2.3.4"}})
	birthdayWebRTCCheck(t, err)
	birthdayWebRTCCheck(t, wan.AddNet(serverNet))
	wan.AddChunkFilter(func(c vnet.Chunk) bool {
		src, dst := c.SourceAddr().(*net.UDPAddr), c.DestinationAddr().(*net.UDPAddr)
		m := &stun.Message{Raw: c.UserData()}
		wire.mu.Lock()
		defer wire.mu.Unlock()
		if m.Decode() == nil && m.Type == stun.BindingRequest {
			if sent, ok := wire.writes[m.TransactionID]; ok {
				if src.IP.String() == "27.1.1.1" && sent.hasRole {
					wire.macBases[src.String()] = sent
				}
				if src.IP.String() == "28.1.1.1" && dst.IP.String() == "27.1.1.1" && sent.pool {
					select {
					case wire.mapping <- birthdayWebRTCMapping{src, dst, sent.conn.LocalAddr().String()}:
					default:
					}
				}
			}
		}
		// Initially only TURN works. Afterwards only the chosen pool mapping
		// admits direct packets, so another original socket cannot satisfy the
		// test accidentally through the production random probe worker.
		if src.IP.String() == "27.1.1.1" && dst.IP.String() == "28.1.1.1" {
			return wire.allowed != "" && dst.String() == wire.allowed
		}
		return true
	})
	birthdayWebRTCCheck(t, wan.Start())
	t.Cleanup(func() { _ = wan.Stop() })
	listener, err := serverNet.ListenPacket("udp4", "1.2.3.4:3478")
	birthdayWebRTCCheck(t, err)
	server, err := turn.NewServer(turn.ServerConfig{
		Realm: "birthday.test", LoggerFactory: logger,
		AuthHandler: func(a *turn.RequestAttributes) (string, []byte, bool) {
			return a.Username, turn.GenerateAuthKey(a.Username, a.Realm, "test-pass"), a.Username == "test-user"
		},
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: listener, RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
			RelayAddress: net.ParseIP("1.2.3.4"), Address: "0.0.0.0", Net: serverNet,
		}}},
	})
	birthdayWebRTCCheck(t, err)
	t.Cleanup(func() { _ = server.Close() })
	return &birthdayWebRTCNet{Net: mac, wire: wire}, &birthdayWebRTCNet{Net: phone, wire: wire, phone: true}
}

// The existing PeerConnections, DTLS keys, SRTP stream, Viewer receiver and
// input DataChannel must all survive promotion of a temporary phone socket.
// One authenticated virtual-network probe guarantees a hit deterministically;
// it neither changes production randomness nor supplies a product test option.
func TestBirthdayAdoptionPreservesWebRTCMediaAndInput(t *testing.T) {
	wire := &birthdayWebRTCWire{writes: make(map[[stun.TransactionIDSize]byte]birthdayWebRTCWrite),
		macBases: make(map[string]birthdayWebRTCWrite), mapping: make(chan birthdayWebRTCMapping, 1)}
	macNet, phoneNet := birthdayWebRTCNets(t, wire)
	makePeer := func(network transport.Net, phone bool) *webrtc.PeerConnection {
		api, err := newAPI(nil, &ice.BoundedNATPunchNet{Net: network, Report: func(message string) {
			if phone && strings.HasPrefix(message, "Birthday pool started:") {
				wire.poolStart.Store(true)
			}
			if phone && strings.Contains(message, "Birthday stopped:") && strings.Contains(message, "adopted=1") {
				wire.adopted.Store(true)
			}
		}}, nil)
		birthdayWebRTCCheck(t, err)
		pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: []webrtc.ICEServer{{
			URLs: []string{"turn:1.2.3.4:3478?transport=udp"}, Username: "test-user", Credential: "test-pass",
		}}})
		birthdayWebRTCCheck(t, err)
		t.Cleanup(func() { _ = pc.Close() })
		return pc
	}
	mac, phone := makePeer(macNet, false), makePeer(phoneNet, true)
	frames := make(chan []byte, 16)
	viewer := &Viewer{pc: phone, done: make(chan struct{}), cfg: ViewerConfig{
		Frame: func(_ bool, data []byte, _ *Trace) { frames <- data },
	}}
	t.Cleanup(func() { close(viewer.done) })
	phone.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { go viewer.receive(track) })
	track, err := webrtc.NewTrackLocalStaticSample(h264, "screen", "screen")
	birthdayWebRTCCheck(t, err)
	sender, err := mac.AddTrack(track)
	birthdayWebRTCCheck(t, err)
	go func() {
		for {
			if _, _, err := sender.ReadRTCP(); err != nil {
				return
			}
		}
	}()
	_, err = phone.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})
	birthdayWebRTCCheck(t, err)
	input := make(chan string, 4)
	mac.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnMessage(func(m webrtc.DataChannelMessage) { input <- string(m.Data) })
	})
	dc, err := phone.CreateDataChannel(inputChannel, nil)
	birthdayWebRTCCheck(t, err)
	ready := make(chan struct{})
	dc.OnOpen(func() { close(ready) })
	local := func(pc *webrtc.PeerConnection, description webrtc.SessionDescription) webrtc.SessionDescription {
		done := webrtc.GatheringCompletePromise(pc)
		birthdayWebRTCCheck(t, pc.SetLocalDescription(description))
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("virtual candidate gathering timed out")
		}
		return *pc.LocalDescription()
	}
	offer, err := phone.CreateOffer(nil)
	birthdayWebRTCCheck(t, err)
	phoneSDP := local(phone, offer)
	birthdayWebRTCCheck(t, mac.SetRemoteDescription(phoneSDP))
	answer, err := mac.CreateAnswer(nil)
	birthdayWebRTCCheck(t, err)
	macSDP := local(mac, answer)
	birthdayWebRTCCheck(t, phone.SetRemoteDescription(macSDP))
	select {
	case <-ready:
	case <-time.After(8 * time.Second):
		t.Fatal("TURN DTLS/input channel did not connect")
	}
	pair := func(pc *webrtc.PeerConnection) *webrtc.ICECandidatePair {
		p, err := pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
		birthdayWebRTCCheck(t, err)
		return p
	}
	relayed := func(p *webrtc.ICECandidatePair) bool {
		return p != nil && (p.Local.Typ == webrtc.ICECandidateTypeRelay || p.Remote.Typ == webrtc.ICECandidateTypeRelay)
	}
	if !relayed(pair(phone)) {
		t.Fatal("baseline must carry data through TURN")
	}
	transfer := func(fill byte, text string) {
		sent := [][]byte{keyframe(fill), delta(fill + 1), delta(fill + 2)}
		for _, frame := range sent {
			birthdayWebRTCCheck(t, track.WriteSample(media.Sample{Data: frame, Duration: time.Second / 30}))
		}
		birthdayWebRTCCheck(t, dc.SendText(text))
		for _, frame := range sent {
			select {
			case got := <-frames:
				if !bytes.Equal(got, frame) {
					t.Fatal("Viewer frame differs after transport")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Viewer did not receive SRTP frame")
			}
		}
		select {
		case got := <-input:
			if got != text {
				t.Fatal("input DataChannel payload differs")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("input DataChannel payload missing")
		}
	}
	transfer(0xa1, "input before socket adoption")
	var hit birthdayWebRTCMapping
	select {
	case hit = <-wire.mapping:
	case <-time.After(5 * time.Second):
		t.Fatal("phone did not open a production socket pool")
	}
	var base birthdayWebRTCWrite
	birthdayWebRTCEventually(t, func() bool {
		wire.mu.Lock()
		defer wire.mu.Unlock()
		var ok bool
		base, ok = wire.macBases[hit.target.String()]
		return ok
	}, time.Second, "missing Mac public mapping's actual ICE socket")
	phonePwd := ""
	for _, line := range strings.Split(phoneSDP.SDP, "\n") {
		if pwd, ok := strings.CutPrefix(strings.TrimSpace(line), "a=ice-pwd:"); ok {
			phonePwd = pwd
			break
		}
	}
	if phonePwd == "" {
		t.Fatal("test SDP has no ICE password")
	}
	request, err := stun.Build(stun.BindingRequest, stun.TransactionID,
		stun.NewUsername(iceUfrag(phoneSDP.SDP)+":"+iceUfrag(macSDP.SDP)), base.controlled, base.priority,
		stun.NewShortTermIntegrity(phonePwd), stun.Fingerprint)
	birthdayWebRTCCheck(t, err)
	wire.mu.Lock()
	wire.allowed = hit.phone.String()
	wire.mu.Unlock()
	_, err = base.conn.WriteTo(request.Raw, hit.phone)
	birthdayWebRTCCheck(t, err)
	birthdayWebRTCEventually(t, func() bool {
		phonePair, macPair := pair(phone), pair(mac)
		return wire.adopted.Load() && phonePair != nil && macPair != nil && !relayed(phonePair) && !relayed(macPair)
	}, 6*time.Second, "production socket adoption did not upgrade both ICE transports")
	chosen := pair(phone)
	_, port, err := net.SplitHostPort(hit.local)
	birthdayWebRTCCheck(t, err)
	if net.JoinHostPort(chosen.Local.Address, port) != hit.local || chosen.Local.Port != hit.phoneLocalPort(t) {
		t.Fatal("direct transport did not select the adopted socket")
	}
	transfer(0xb1, "input after socket adoption")
	t.Log("same DTLS/SRTP stream and input DataChannel transferred frames and input through TURN and the adopted direct socket")
}

func (m birthdayWebRTCMapping) phoneLocalPort(t *testing.T) uint16 {
	t.Helper()
	address, err := net.ResolveUDPAddr("udp4", m.local)
	birthdayWebRTCCheck(t, err)
	return uint16(address.Port)
}
