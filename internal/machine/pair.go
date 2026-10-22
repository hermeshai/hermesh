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
	"golang.org/x/term"
)

// Pair registers this machine with the owner: it shows a pairing code and
// waits until the owner approves it. address is a connection address (the
// owner is pinned from it) or a hosted center URL (the owner is learned from
// the center's claim and checked with a verification code).
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
	// A machine whose owner its center no longer serves (the owner identity
	// was reset) pairs with whichever owner comes next; its projects stay.
	ownerGone := n.Paired() && OwnerGone(ctx, n.cfg.HTTPClient, n.RelayURL(), n.ownerID)
	if ownerGone {
		fmt.Fprintf(out, "The previous owner %s is gone from its center (it was reset); pairing with a new owner.\n", n.ownerID)
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
		Hostname: host, Platform: runtime.GOOS + "/" + runtime.GOARCH, OwnerId: ownerID, CreatedAt: timestamppb.Now()}
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
	fmt.Fprintf(out, "Paired as %q (%s).\n", n.Name(), n.machineID)
	return nil
}

// OwnerGone reports whether the hosted center at relayURL says it no longer
// serves ownerID. Only a hosted center answers that; an unreachable one or
// a self-hosted relay does not count as gone.
func OwnerGone(ctx context.Context, hc connect.HTTPClient, relayURL, ownerID string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := relayv1connect.NewRelayPublicServiceClient(hc, strings.TrimSuffix(relayURL, "/")).GetInfo(ctx, &relayv1.GetInfoRequest{OwnerId: ownerID})
	return err == nil && info.Hosted && info.OwnerId != ownerID
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

// PairOptions decides whether a machine accepts credentials from an owner it
// learned from a hosted center's claim (an owner pinned from a connection
// address is always checked against the pin instead; a client accepts the
// owner of the account that claimed it: see PairWithRelay).
type PairOptions struct {
	// Confirm asks the person at the requester whether their own signer showed
	// this verification code and they approved it. A compromised center can
	// claim the request for an attacker's owner, whose signer then approves;
	// only this question catches that.
	Confirm func(ownerID, code string) bool
	// TrustClaim accepts the center's claim without asking (--yes): the
	// center is then trusted to have claimed for the right owner.
	TrustClaim bool
}

// PairWithRelay submits a self-signed pairing request and waits for the
// owner to approve it, returning the credentials and the owner id. Re-running
// shows the same code (it is derived from the key).
//
// With a hosted center the request may name no owner. When an account
// claims it, the center reports the owner; that report is not trusted: the
// requester shows the verification code for its key and that owner, the
// owner's signer shows the code for the key it received and its own owner,
// and the owner approves only if they match. Because a compromised center
// could claim for an attacker's owner instead, an unpinned requester then
// also needs PairOptions to accept the result. The credentials must chain to
// the owner the code was shown for.
//
// A client (an agent) with no owner pinned is claimed through a link
// instead: it shows a link to the center's console, the person opens it
// signed in to their account and sends the request to their Mac, and the
// client accepts that account's owner without a code to compare. On this
// first pairing it trusts the center to report the right owner; from then on
// it is pinned to it. A compromised center could thereby point a new agent
// at an owner of its own, never at the owner's machines: they only take
// grants that chain to the owner's own root key.
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
	if link != "" {
		fmt.Fprintf(out, "To approve %s, open this link and sign in:\n  %s\nWaiting for approval...\n", pr.Label, link)
	} else if hosted {
		valid := res.ExpiresAt.AsTime().Local().Format("15:04")
		if pinned != "" {
			// The request reaches the owner's devices by itself: nothing to
			// type, only the code to compare.
			fmt.Fprintf(out, "Verification code: %s (valid until %s)\nApprove only if Hermesh on your Mac shows this same code.\n",
				code(pinned), valid)
		} else {
			fmt.Fprintf(out, "Pairing code: %s\nIn Hermesh on your Mac, choose \"Enter Pairing Code…\" and type it (valid until %s).\n",
				res.Code, valid)
		}
		fmt.Fprintln(out, "Waiting...")
	} else {
		fmt.Fprintf(out, "Pairing code: %s\nEnter it in Hermesh.app under \"Add %s\" (valid until %s).\nWaiting for approval...\n",
			res.Code, what, res.ExpiresAt.AsTime().Local().Format("15:04"))
	}
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
			by := ""
			if w.AccountHint != "" {
				by = " by " + w.AccountHint
			}
			if link != "" {
				who := ""
				if w.AccountHint != "" {
					who = " (" + w.AccountHint + ")"
				}
				fmt.Fprintf(out, "Sent to Hermesh on your Mac%s; approve it there.\n", who)
				continue
			}
			fmt.Fprintf(out, "Claimed%s (owner %s).\nVerification code: %s\nApprove only if Hermesh on your Mac shows this same code.\nWaiting for approval...\n",
				by, owner, code(owner))
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
				case opts.TrustClaim:
					fmt.Fprintf(out, "Accepting owner %s on the center's word (--yes).\n", owner)
				case opts.Confirm != nil && opts.Confirm(owner, code(owner)):
				case opts.Confirm != nil:
					return nil, "", errors.New("not confirmed; nothing was accepted")
				default:
					return nil, "", fmt.Errorf("cannot confirm owner %s without a terminal: pair with the connection address shown in Hermesh on your Mac (pins the owner), or pass --yes to trust the center's claim", owner)
				}
			}
			return w.Credentials, owner, nil
		}
		if w.Expired {
			return nil, "", errors.New("the pairing code expired; run the command again for a new one")
		}
	}
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

// TerminalConfirm asks on the terminal when stdin is one; nil otherwise.
func TerminalConfirm(in *os.File, out io.Writer) func(ownerID, code string) bool {
	if !term.IsTerminal(int(in.Fd())) {
		return nil
	}
	return func(ownerID, code string) bool {
		fmt.Fprintf(out, "Did Hermesh on your Mac ask you to approve code %s, and did you approve it? [y/N] ", code)
		var answer string
		fmt.Fscanln(in, &answer)
		return strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
	}
}
