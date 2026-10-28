package machine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/internal/connaddr"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Pair registers this machine with the owner: it shows a pairing code and
// waits until the owner approves it. address is a connection address (the
// owner is pinned from it) or a hosted center URL (the owner is learned from
// the center's claim and proven by the two digits, see PairWithRelay).
func (n *Machine) Pair(ctx context.Context, address, label string, out io.Writer, opts PairOptions) error {
	relayURL, ownerID, err := connaddr.ParseAny(address)
	if err != nil {
		return err
	}
	if ownerID == "" {
		// A hosted center's devices may connect elsewhere than its console.
		if relayURL, err = connaddr.Discover(ctx, n.cfg.HTTPClient, relayURL); err != nil {
			return err
		}
	}
	// A machine whose owner its relay no longer serves (the owner erased
	// everything, or its identity was reset) pairs with whichever owner comes
	// next; its projects stay.
	ownerGone := n.Paired() && OwnerGone(ctx, n.cfg.HTTPClient, n.RelayURL(), n.ownerID)
	if ownerGone {
		fmt.Fprintf(out, "The previous owner %s is gone from its server (it erased everything, or was reset); pairing with a new owner.\n", n.ownerID)
	}
	if n.Paired() && !ownerGone && ownerID != "" && n.ownerID != ownerID {
		return fmt.Errorf("this machine is already paired with owner %s", n.ownerID)
	}
	host, _ := os.Hostname()
	if label == "" {
		label = HostLabel("")
	}
	spki := identity.MarshalPublicKey(n.key.Public())
	pr := &identityv1.PairingRequest{Kind: identityv1.PairingKind_PAIRING_KIND_MACHINE, SubjectPublicKey: spki, Label: label,
		Hostname: host, Platform: runtime.GOOS + "/" + runtime.GOARCH, OwnerId: ownerID, CreatedAt: timestamppb.Now(),
		// The two digits, when there is someone to type them.
		ConfirmMatch: ownerID == "" && opts.Digits != nil && !opts.TrustClaim}
	creds, ownerID, err := PairWithRelay(ctx, n.cfg.HTTPClient, relayURL, pr, n.key, out, opts)
	if err != nil {
		return err
	}
	if n.Paired() && !ownerGone && n.ownerID != ownerID {
		return fmt.Errorf("this machine is already paired with owner %s", n.ownerID)
	}
	if err := n.Install(ownerID, relayURL, creds.RootChain, creds.GetMachineCert()); err != nil {
		return err
	}
	// The machine names itself: the name it asked with (its operator's
	// --name or its host name). The owner can rename it later.
	if err := n.SetName(label); err != nil {
		return err
	}
	fmt.Fprintf(out, "✓ Paired as “%s” (%s)\n", n.Name(), n.machineID)
	return nil
}

// OwnerGone reports whether the relay or center at relayURL says it no
// longer serves ownerID: the owner erased everything, or its identity was
// reset (a center), or another owner claimed the relay. An unreachable one
// does not count as gone.
func OwnerGone(ctx context.Context, hc connect.HTTPClient, relayURL, ownerID string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := relayv1connect.NewRelayPublicServiceClient(hc, strings.TrimSuffix(relayURL, "/")).GetInfo(ctx, &relayv1.GetInfoRequest{OwnerId: ownerID})
	return err == nil && info.OwnerId != ownerID
}

// HostLabel is a default name from this host's name: prefix plus its first
// DNS label, at most 64 bytes (CI runners and some clouds use host names
// longer than a relay accepts as a label).
func HostLabel(prefix string) string {
	host, _ := os.Hostname()
	return hostLabel(prefix, host)
}

func hostLabel(prefix, host string) string {
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	s := prefix + host
	for len(s) > 64 {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return s
}

// Install verifies and stores the owner trust anchor and this machine's
// certificate (used by pairing and by the owner's own Mac).
func (n *Machine) Install(ownerID, relayURL string, rootChain *identityv1.RootChain, machineCert *identityv1.Signed) error {
	trust := identity.NewTrust(ownerID)
	if _, err := trust.AcceptRootChain(rootChain); err != nil {
		return fmt.Errorf("owner root chain: %w", err)
	}
	_, pub, err := trust.VerifyMachineCert(machineCert)
	if err != nil {
		return err
	}
	if !pub.Equal(n.key.Public()) {
		return errors.New("received a certificate for a different key")
	}
	for k, v := range map[string]string{kOwnerID: ownerID, kRelayURL: relayURL} {
		if err := n.st.Set(k, []byte(v)); err != nil {
			return err
		}
	}
	n.st.SetProto(kRootChain, rootChain)
	n.st.SetProto(kMachineCert, machineCert)
	n.mu.Lock()
	n.ownerID, n.relayURL, n.trust = ownerID, relayURL, trust
	n.machineCert = machineCert
	n.mu.Unlock()
	n.audit.Log(AuditRecord{Kind: "paired", Subject: ownerID})
	return nil
}

// PublicKeySPKI returns this machine's public key (PKIX DER).
func (n *Machine) PublicKeySPKI() []byte { return identity.MarshalPublicKey(n.key.Public()) }

// PairOptions decides how a machine accepts credentials from an owner it
// learned from a hosted center's claim (an owner pinned from a connection
// address is always checked against the pin instead; a client accepts the
// owner of the account that claimed it: see PairWithRelay).
type PairOptions struct {
	// Digits reads what the person types at the requester, nil without a
	// terminal. With it, an unpinned machine asks for the two digits the
	// approving device shows (PairingRequest.confirm_match): a center that
	// claimed the request for another owner cannot make the person's own
	// device show them.
	Digits func(prompt string) (string, error)
	// TrustClaim accepts the center's claim without asking (--yes): the
	// center is then trusted to have claimed for the right owner.
	TrustClaim bool
	// QR draws the request's link as a QR code in out (a terminal).
	QR bool
}

// matchTries is how many times the person may type the two digits.
const matchTries = 3

// resubmitRounds bounds how often an expired request is submitted again
// while the requester waits (about an hour with the relay's 10 minutes).
const resubmitRounds = 6

// PairWithRelay submits a self-signed pairing request and waits for the
// owner to approve it, returning the credentials and the owner id. Re-running
// shows the same code (it is derived from the key), and so does a request
// submitted again once it expired while this waits.
//
// With a hosted center the request may name no owner. When an account
// claims it, the center reports the owner; that report is not trusted. An
// unpinned machine that can ask (opts.Digits) asks for the two digits of the
// machine cert the person's device signed; the code it shows already binds
// its key, since the device checks the request against it. Without a
// terminal it needs opts.TrustClaim. The credentials must chain to the owner
// the center reported.
//
// A client (an agent) with no owner pinned is claimed through a link
// instead: it shows a link to the center's console, the person opens it
// signed in to their account and sends the request to their devices, and
// the client accepts that account's owner without a code to compare. On
// this first pairing it trusts the center to report the right owner; from
// then on it is pinned to it. A compromised center could thereby point a
// new agent at an owner of its own, never at the owner's machines: they only
// take grants that chain to the owner's own root key.
func PairWithRelay(ctx context.Context, hc connect.HTTPClient, relayURL string, pr *identityv1.PairingRequest,
	key identity.Signer, out io.Writer, opts PairOptions) (*identityv1.Credentials, string, error) {
	signed, err := identity.Sign(pr, key)
	if err != nil {
		return nil, "", err
	}
	pub := relayv1connect.NewRelayPublicServiceClient(hc, relayURL)
	res, err := pub.SubmitPairing(ctx, &relayv1.SubmitPairingRequest{Request: signed})
	if err != nil {
		return nil, "", fmt.Errorf("submit pairing request: %w", err)
	}
	what := "machine"
	if pr.Kind == identityv1.PairingKind_PAIRING_KIND_CLIENT {
		what = "client"
	}
	info, _ := pub.GetInfo(ctx, &relayv1.GetInfoRequest{})
	hosted := info.GetHosted()
	pinned := pr.OwnerId
	owner := pinned
	code := func(o string) string { return identity.VerificationCode(pr.Kind, pr.SubjectPublicKey, o) }
	link := ""
	if hosted && pinned == "" && pr.Kind == identityv1.PairingKind_PAIRING_KIND_CLIENT {
		link = PairingLink(info.GetConsoleUrl(), res.Code)
	}
	switch {
	case link != "":
		fmt.Fprintf(out, "To approve %s, open this link and sign in:\n  %s\n", pr.Label, link)
		if opts.QR {
			fmt.Fprint(out, QRCode(link, nil))
		}
		fmt.Fprintln(out, "Waiting for approval...")
	case hosted && pinned != "":
		// The request reaches the owner's devices by itself: nothing to
		// type, only the code to compare.
		fmt.Fprintf(out, "Verification code: %s (valid until %s)\nApprove only if Hermesh on your iPhone or Mac shows this same code.\nWaiting...\n",
			code(pinned), res.ExpiresAt.AsTime().Local().Format("15:04"))
	case hosted:
		scan := PairingLink(info.GetConsoleUrl(), res.Code)
		fmt.Fprintln(out, "\nPair this machine with your Hermesh")
		side := []string{"Scan it with Hermesh on your iPhone,", "or enter the pairing code in Hermesh:", "", "  " + res.Code}
		if opts.QR && scan != "" {
			fmt.Fprint(out, QRCode(scan, side))
		} else {
			fmt.Fprintf(out, "Pairing code: %s\nEnter it in Hermesh on your iPhone or Mac.\n", res.Code)
		}
		fmt.Fprintln(out, "Waiting...")
	default:
		fmt.Fprintf(out, "Pairing code: %s\nEnter it in the Hermesh app to add this %s (valid until %s).\nWaiting for approval...\n",
			res.Code, what, res.ExpiresAt.AsTime().Local().Format("15:04"))
	}
	rounds := 0
	for {
		w, err := pub.WaitPairing(ctx, &relayv1.WaitPairingRequest{Code: res.Code, KnownOwnerId: owner})
		if err != nil {
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			time.Sleep(2 * time.Second)
			continue
		}
		if w.Claimed {
			if owner != "" && w.OwnerId != owner {
				return nil, "", fmt.Errorf("the center claimed this request for a different owner (%s, expected %s); it may be compromised; nothing was accepted", w.OwnerId, owner)
			}
			if _, err := identity.NewTrust(w.OwnerId).AcceptRootChain(w.RootChain); err != nil {
				return nil, "", fmt.Errorf("the center reported an owner that does not check out: %w", err)
			}
			owner = w.OwnerId
			who := ""
			if w.AccountHint != "" {
				who = " (" + w.AccountHint + ")"
			}
			switch {
			case link != "":
				fmt.Fprintf(out, "Sent to your trusted devices%s; approve it in the Hermesh app.\n", who)
			case pr.ConfirmMatch:
				fmt.Fprintf(out, "✓ Claimed%s: approve it in Hermesh on your iPhone or Mac\n", who)
			default:
				by := ""
				if w.AccountHint != "" {
					by = " by " + w.AccountHint
				}
				fmt.Fprintf(out, "Claimed%s (owner %s).\nVerification code: %s\nApprove only if Hermesh on your iPhone or Mac shows this same code.\nWaiting for approval...\n",
					by, owner, code(owner))
			}
			continue
		}
		if w.Done {
			if owner == "" {
				return nil, "", errors.New("the center returned credentials without a claim; pair again")
			}
			if identity.ChainOwner(w.GetCredentials().GetRootChain()) != owner {
				return nil, "", errors.New("the credentials are from a different owner than the one shown; pair again")
			}
			if pinned == "" && link == "" {
				switch {
				case pr.ConfirmMatch && opts.Digits != nil:
					if err := confirmMatch(w.Credentials, opts.Digits, out); err != nil {
						return nil, "", err
					}
				case opts.TrustClaim:
					fmt.Fprintf(out, "Accepting owner %s on the center's word (--yes).\n", owner)
				default:
					return nil, "", fmt.Errorf("cannot confirm owner %s without a terminal: pair with the connection address shown in the Hermesh app (pins the owner), or pass --yes to trust the center's claim", owner)
				}
			}
			return w.Credentials, owner, nil
		}
		if w.Expired {
			if rounds++; rounds > resubmitRounds {
				return nil, "", errors.New("the pairing code expired; run the command again for a new one")
			}
			owner = pinned
			// The same code again (it comes from the key): a pinned request
			// reaches the owner's devices again, an unpinned one waits to be
			// scanned or typed.
			if res, err = pub.SubmitPairing(ctx, &relayv1.SubmitPairingRequest{Request: signed}); err != nil {
				return nil, "", fmt.Errorf("submit pairing request: %w", err)
			}
		}
	}
}

// confirmMatch has the person type the two digits the approving device
// shows, computed from the machine cert it signed.
func confirmMatch(creds *identityv1.Credentials, digits func(string) (string, error), out io.Writer) error {
	cert := creds.GetMachineCert()
	want := identity.JoinMatchCode(cert.GetBody())
	fmt.Fprintf(out, "✓ Approved on %s\n", approver(creds))
	for i := 0; i < matchTries; i++ {
		got, err := digits("  Type the two digits it shows: ")
		if err != nil {
			return fmt.Errorf("reading the two digits: %w", err)
		}
		if strings.TrimSpace(got) == want {
			return nil
		}
		left := matchTries - 1 - i
		if left == 0 {
			break
		}
		fmt.Fprintf(out, "✗ Those aren’t the digits your device shows. %d %s left.\n", left, map[bool]string{true: "try", false: "tries"}[left == 1])
	}
	return errors.New("not the digits the approving device shows; nothing was installed. If you didn't approve this machine, remove it in the Hermesh app")
}

// approver names the trusted device that signed the machine cert, by its
// key's label in the root chain.
func approver(creds *identityv1.Credentials) string {
	tr := identity.NewTrust(identity.ChainOwner(creds.GetRootChain()))
	if _, err := tr.AcceptRootChain(creds.GetRootChain()); err == nil {
		for _, rk := range tr.Root().RootKeys {
			pub, err := identity.ParsePublicKey(rk.PublicKey)
			if err == nil && identity.KeyID(pub) == creds.GetMachineCert().GetSignerKeyId() && rk.Label != "" {
				return "“" + rk.Label + "”"
			}
		}
	}
	return "your device"
}

// PairingLink is where a person approves a client's pairing request: the
// pairing page at the root of the hosted center's console (consoleURL is
// any page of it), "" without one.
func PairingLink(consoleURL, code string) string {
	u, err := url.Parse(consoleURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/p/" + code
}
