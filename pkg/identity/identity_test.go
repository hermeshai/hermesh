package identity

import (
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
)

// testOwner is an owner whose trusted devices are software keys; chain is
// its root chain so far.
type testOwner struct {
	first   *SoftwareKey
	ownerID string
	chain   *identityv1.RootChain
}

func newTestOwner(t *testing.T) *testOwner {
	t.Helper()
	first, _ := GenerateKey()
	o := &testOwner{first: first, ownerID: OwnerID(MarshalPublicKey(first.Public())), chain: &identityv1.RootChain{}}
	o.extend(t, first, first)
	return o
}

// extend appends the next root cert, listing devices, signed by signer.
func (o *testOwner) extend(t *testing.T, signer *SoftwareKey, devices ...*SoftwareKey) *identityv1.RootChain {
	t.Helper()
	o.chain = &identityv1.RootChain{Certs: append(slices.Clone(o.chain.Certs), o.cert(t, uint64(len(o.chain.Certs)+1), signer, devices...))}
	return o.chain
}

func (o *testOwner) cert(t *testing.T, version uint64, signer *SoftwareKey, devices ...*SoftwareKey) *identityv1.Signed {
	t.Helper()
	rc := &identityv1.RootCert{OwnerId: o.ownerID, Version: version, IssuedAt: timestamppb.Now()}
	for _, d := range devices {
		rc.RootKeys = append(rc.RootKeys, &identityv1.RootKey{PublicKey: MarshalPublicKey(d.Public()), Label: "mac", Platform: "macos"})
	}
	s, err := Sign(rc, signer)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (o *testOwner) grant(t *testing.T, signer Signer, client *SoftwareKey, ttl time.Duration) *identityv1.Signed {
	t.Helper()
	spki := MarshalPublicKey(client.Public())
	g := &identityv1.Grant{OwnerId: o.ownerID, GrantId: "g1", Version: 1, ClientId: ClientID(spki), ClientPublicKey: spki,
		MachineIds: []string{"*"}, ProjectIds: []string{"*"}, Agents: []string{"claude"},
		NotBefore: timestamppb.Now(), ExpiresAt: timestamppb.New(time.Now().Add(ttl))}
	s, err := Sign(g, signer)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGrantChain(t *testing.T) {
	o := newTestOwner(t)
	tr := NewTrust(o.ownerID)
	if _, err := tr.AcceptRootChain(o.chain); err != nil {
		t.Fatal(err)
	}
	client, _ := GenerateKey()
	g := o.grant(t, o.first, client, time.Hour)
	sub, err := tr.VerifyCredentials(&identityv1.Credentials{RootChain: o.chain, Subject: &identityv1.Credentials_Grant{Grant: g}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if sub.Kind != SubjectClient || !sub.PublicKey.Equal(client.Public()) {
		t.Fatal("wrong subject")
	}

	// Signed by a non-root key (e.g. a malicious relay): rejected.
	attacker, _ := GenerateKey()
	if _, _, err := tr.VerifyGrant(o.grant(t, attacker, client, time.Hour), time.Now()); err == nil {
		t.Fatal("grant signed by attacker accepted")
	}
	// Expired.
	if _, _, err := tr.VerifyGrant(o.grant(t, o.first, client, -time.Hour), time.Now()); err == nil {
		t.Fatal("expired grant accepted")
	}
	// Tampered body.
	g2 := o.grant(t, o.first, client, time.Hour)
	g2.Body[len(g2.Body)-1] ^= 1
	if _, _, err := tr.VerifyGrant(g2, time.Now()); err == nil {
		t.Fatal("tampered grant accepted")
	}
}

// TestRootChain: devices join and leave only with the approval of a device
// already trusted; removed devices sign nothing any more; forks, gaps,
// rollbacks and foreign chains are refused (docs/trusted-devices.md 3).
func TestRootChain(t *testing.T) {
	o := newTestOwner(t)
	a := o.first
	tr := NewTrust(o.ownerID)
	if changed, err := tr.AcceptRootChain(o.chain); err != nil || !changed {
		t.Fatalf("v1: changed=%v err=%v", changed, err)
	}
	v1 := o.chain
	client, _ := GenerateKey()
	byA := o.grant(t, a, client, time.Hour)

	// B joins, approved by A: both sign.
	b, _ := GenerateKey()
	v2 := o.extend(t, a, a, b)
	if changed, err := tr.AcceptRootChain(v2); err != nil || !changed {
		t.Fatalf("join: changed=%v err=%v", changed, err)
	}
	if _, _, err := tr.VerifyGrant(o.grant(t, b, client, time.Hour), time.Now()); err != nil {
		t.Fatalf("grant by the device that joined: %v", err)
	}
	// An older chain the held one extends is fine and changes nothing.
	if changed, err := tr.AcceptRootChain(v1); err != nil || changed {
		t.Fatalf("older chain: changed=%v err=%v", changed, err)
	}

	// A fork at v2 (A adding someone else instead) is refused.
	mallory, _ := GenerateKey()
	fork := &identityv1.RootChain{Certs: []*identityv1.Signed{v1.Certs[0], o.cert(t, 2, a, a, mallory)}}
	if _, err := tr.AcceptRootChain(fork); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("fork: %v", err)
	}

	// A is removed by B: what A signed stops verifying, and A cannot extend.
	v3 := o.extend(t, b, b)
	if _, err := tr.AcceptRootChain(v3); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tr.VerifyGrant(byA, time.Now()); err == nil {
		t.Fatal("a grant by the removed device still verifies")
	}
	byRemoved := &identityv1.RootChain{Certs: append(slices.Clone(v3.Certs), o.cert(t, 4, a, a, b))}
	if _, err := tr.AcceptRootChain(byRemoved); err == nil {
		t.Fatal("the removed device extended the chain")
	}

	// Gaps, a first cert not naming the owner, and other owners' chains.
	gap := &identityv1.RootChain{Certs: append(slices.Clone(v3.Certs), o.cert(t, 5, b, b))}
	if _, err := tr.AcceptRootChain(gap); err == nil {
		t.Fatal("a version gap was accepted")
	}
	if _, err := VerifyRootChain(&identityv1.RootChain{Certs: []*identityv1.Signed{o.cert(t, 1, b, b)}}, o.ownerID); err == nil {
		t.Fatal("a v1 listing another key than the owner's was accepted")
	}
	other := newTestOwner(t)
	if _, err := NewTrust(o.ownerID).AcceptRootChain(other.chain); err == nil {
		t.Fatal("another owner's chain was accepted")
	}
	// A fresh verifier takes the whole chain at once.
	fresh := NewTrust(o.ownerID)
	if _, err := fresh.AcceptRootChain(v3); err != nil || fresh.RootVersion() != 3 {
		t.Fatalf("whole chain: v%d %v", fresh.RootVersion(), err)
	}
}

func TestPairingCode(t *testing.T) {
	k, _ := GenerateKey()
	spki := MarshalPublicKey(k.Public())
	c := PairingCode(identityv1.PairingKind_PAIRING_KIND_CLIENT, spki)
	if len(c) != 14 || strings.Count(c, "-") != 2 {
		t.Fatalf("bad code %q", c)
	}
	if NormalizePairingCode(strings.ToLower(strings.ReplaceAll(c, "-", " "))) != c {
		t.Fatal("normalize failed")
	}
	if PairingCode(identityv1.PairingKind_PAIRING_KIND_MACHINE, spki) == c {
		t.Fatal("kind not bound into code")
	}
	k2, _ := GenerateKey()
	if PairingCode(identityv1.PairingKind_PAIRING_KIND_CLIENT, MarshalPublicKey(k2.Public())) == c {
		t.Fatal("different keys share a code")
	}
}

func TestTLSCredentialsRoundTrip(t *testing.T) {
	k, _ := GenerateKey()
	creds := &identityv1.Credentials{RootChain: &identityv1.RootChain{Certs: []*identityv1.Signed{{Type: "x"}}}}
	cert, err := TLSCertificate(k.CryptoSigner(), creds)
	if err != nil {
		t.Fatal(err)
	}
	pub, got, err := PeerCredentials(cert.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	if !pub.Equal(k.Public()) || got.RootChain.Certs[0].Type != "x" {
		t.Fatal("round trip mismatch")
	}
}

// TestJoinMatchCode pins the digits (the Mac app computes them too; its
// --check-expected checks the same vector).
func TestJoinMatchCode(t *testing.T) {
	if got := JoinMatchCode([]byte("hermesh")); got != "42" {
		t.Fatalf("JoinMatchCode = %q", got)
	}
	if len(JoinMatchCode(nil)) != 2 {
		t.Fatal("not two digits")
	}
}

func TestVerificationCode(t *testing.T) {
	spki := []byte("fixed test key bytes")
	// Pinned vector: the Mac app (SigningPreview.verificationCode) must
	// produce the same value.
	if got := VerificationCode(identityv1.PairingKind_PAIRING_KIND_MACHINE, spki, "o_testowner"); got != verificationVector {
		t.Fatalf("vector changed: %s", got)
	}
	a := VerificationCode(identityv1.PairingKind_PAIRING_KIND_MACHINE, spki, "o_a")
	for _, other := range []string{
		VerificationCode(identityv1.PairingKind_PAIRING_KIND_MACHINE, spki, "o_b"),
		VerificationCode(identityv1.PairingKind_PAIRING_KIND_CLIENT, spki, "o_a"),
		VerificationCode(identityv1.PairingKind_PAIRING_KIND_MACHINE, append([]byte("x"), spki...), "o_a"),
	} {
		if other == a {
			t.Fatal("verification code ignores owner, kind or key")
		}
	}
	if PairingCode(identityv1.PairingKind_PAIRING_KIND_MACHINE, spki) == a {
		t.Fatal("verification code equals the pairing code")
	}
}

const verificationVector = "K5FG-B5PE-48PS"
