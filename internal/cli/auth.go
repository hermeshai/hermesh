package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	"github.com/hermeshai/hermesh/internal/connaddr"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/pkg/identity"
	"github.com/hermeshai/hermesh/pkg/transport"
)

func (a *app) authCmd() *cobra.Command {
	cmd := group(&cobra.Command{Use: "auth", Short: "Pair this client with an owner, check the pairing, or forget it"})
	cmd.AddCommand(a.loginCmd(), a.authStatusCmd(), a.logoutCmd())
	return cmd
}

// maxLabel is the longest name the relay takes in a pairing request.
const maxLabel = 64

func (a *app) loginCmd() *cobra.Command {
	var name string
	var yes bool
	cmd := &cobra.Command{
		Use:   "login [center URL | connection address]",
		Short: "Pair with an owner: shows a link (or a code) for the owner to approve on their Mac",
		Long: `Pair with an owner, and wait until they approve it on their Mac.

Without an address it pairs through $HERMESH_CENTER, else the official
center (https://hermesh.io): it shows a link that the owner opens, signed in
to their account, to send the request to Hermesh on their Mac. With a
connection address (from Hermesh on the owner's Mac, or a server of their
own) the request goes to that owner directly.

Already paired with this owner, it checks with the relay and does nothing
while the owner still admits this agent; once the owner revoked it, or the
grant expired, it asks again. To pair with another owner, run
` + "`hermesh auth logout`" + ` first, unless the old owner is gone from its
center (it started over): then login pairs with the new one directly.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var arg string
			if len(args) > 0 {
				arg = args[0]
			}
			address := connaddr.Center(arg)
			relayURL, ownerID, err := connaddr.ParseAny(address)
			if err != nil {
				return fail(exitUsage, "%v", err)
			}
			if ownerID == "" {
				// A hosted center's devices may connect elsewhere than its console.
				if relayURL, err = connaddr.Discover(cmd.Context(), a.hc, relayURL); err != nil {
					return fail(exitUnavailable, "%v", err)
				}
			}
			if len(name) > maxLabel {
				return fail(exitUsage, "--name is %d bytes long; at most %d", len(name), maxLabel)
			}
			// In JSON mode stdout has only the final document; the pairing
			// code and the waiting go to stderr.
			progress := cmd.OutOrStdout()
			if a.output == "json" {
				progress = cmd.ErrOrStderr()
			}
			done, previous, err := a.alreadyPaired(cmd.Context(), progress, relayURL, ownerID)
			if err != nil {
				return err
			}
			if done {
				return a.loggedIn(cmd)
			}
			if err := a.loadKey(); err != nil {
				return err
			}
			host, _ := os.Hostname()
			if name == "" {
				name = firstNonEmpty(previous, machine.HostLabel("client@"))
			}
			pr := &identityv1.PairingRequest{Kind: identityv1.PairingKind_PAIRING_KIND_CLIENT, SubjectPublicKey: identity.MarshalPublicKey(a.key.Public()),
				Label: name, Hostname: host, Platform: runtime.GOOS + "/" + runtime.GOARCH, OwnerId: ownerID, CreatedAt: timestamppb.Now()}
			creds, ownerID, err := machine.PairWithRelay(cmd.Context(), a.hc, relayURL, pr, a.key, progress,
				machine.PairOptions{TrustClaim: yes, QR: machine.IsTerminal(progress)})
			if err != nil {
				return err
			}
			a.st = state{RelayURL: relayURL, OwnerID: ownerID}
			a.trust = identity.NewTrust(ownerID)
			if _, err := a.trust.AcceptRootChain(creds.RootChain); err != nil {
				return fmt.Errorf("owner root chain: %w", err)
			}
			if err := a.adopt(creds.RootChain, creds.GetGrant()); err != nil {
				return err
			}
			// Its inbox, so that news and messages can reach it from now on.
			if c, err := a.dial(cmd.Context(), false); err == nil {
				if err := a.registerInbox(cmd.Context(), c); err != nil {
					fmt.Fprintln(os.Stderr, "warning: registering this agent's inbox:", err)
				}
				c.Close()
			}
			fmt.Fprintln(progress, "Paired. Run `hermesh project list` to see where it can work.")
			return a.loggedIn(cmd)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name shown to the owner (default: the current name when pairing again, else client@<hostname>; at most 64 bytes)")
	cmd.Flags().BoolVar(&yes, "yes", false, "with a center that shows a pairing code instead of a link: accept the owner it reports without asking (trusts the center)")
	return cmd
}

// alreadyPaired tells login whether there is nothing to do: this agent is
// paired with that owner and the relay still admits it. Pairing with
// another owner needs a logout first. When the agent pairs again, previous
// is the name it had.
func (a *app) alreadyPaired(ctx context.Context, w io.Writer, relayURL, ownerID string) (done bool, previous string, err error) {
	err = a.load()
	if errors.Is(err, errNotLoggedIn) {
		return false, "", nil
	}
	if err != nil {
		return false, "", &exitError{code: exitUsage, kind: "invalid_state", err: fmt.Errorf("cannot read the pairing stored in %s (%v)", a.home, err),
			hint: "run `hermesh auth logout`, then log in again"}
	}
	if (ownerID != "" && ownerID != a.st.OwnerID || ownerID == "" && relayURL != a.st.RelayURL) &&
		machine.OwnerGone(ctx, a.hc, a.st.RelayURL, a.st.OwnerID) {
		// The owner erased everything, or was reset: nothing to keep, pair with the new one.
		fmt.Fprintf(w, "The previous owner %s is gone from its server (it erased everything, or was reset); pairing with a new owner.\n", a.st.OwnerID)
		previous = a.g.ClientLabel
		_, err := a.forget()
		return false, previous, err
	}
	if ownerID != "" && ownerID != a.st.OwnerID || ownerID == "" && relayURL != a.st.RelayURL {
		return false, "", &exitError{code: exitUsage, kind: "invalid_state", err: fmt.Errorf("already paired with owner %s (%s)", a.st.OwnerID, a.address()),
			hint: "run `hermesh auth logout` first to pair with another owner"}
	}
	previous = a.g.ClientLabel
	if relayURL != a.st.RelayURL {
		return false, previous, nil // the owner's relay moved
	}
	// A pairing that no longer works is replaced by a new agent with a new
	// key: the relay keeps a completed request for the old key a while (so
	// an interrupted login shows the same code again), and the owner keeps
	// the old agent revoked.
	if _, _, err := a.trust.VerifyGrant(a.grant, time.Now()); err != nil {
		fmt.Fprintf(w, "The grant is no longer valid (%v); pairing again as a new client.\n", err)
		_, err := a.forget()
		return false, previous, err
	}
	c, _, err := a.connect(ctx, nil)
	switch {
	case err == nil:
		c.Close()
		fmt.Fprintf(w, "Already paired with owner %s as %q. Run `hermesh auth status` for details.\n", a.st.OwnerID, a.g.ClientLabel)
		return true, previous, nil
	case errors.Is(err, errRevoked), errors.Is(err, transport.ErrAdmission):
		fmt.Fprintf(w, "The owner no longer admits this client (%s); pairing again as a new client.\n", refusal(err))
		_, err := a.forget()
		return false, previous, err
	}
	return false, "", fail(exitUnavailable, "cannot reach the relay to check the current pairing: %v", err)
}

// forget removes this agent's grant and key from this computer. Embedded
// (Exec), the caller keeps them: there are no files.
func (a *app) forget() (removed bool, err error) {
	if a.mem != nil {
		return false, nil
	}
	for _, p := range []string{a.statePath(), a.keyPath()} {
		if err := os.Remove(p); err == nil {
			removed = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
	}
	return removed, nil
}

// loggedIn ends a login: in JSON mode the document with the pairing.
func (a *app) loggedIn(cmd *cobra.Command) error {
	if a.output != "json" {
		return nil
	}
	id, g := a.identityAndGrant()
	return a.report(cmd.OutOrStdout(), nil, field{"auth", &authReport{Identity: id, Grant: g, Checks: []*authCheck{}, Home: a.home, Version: machine.Version}})
}

func (a *app) logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget this client's key and grant on this computer (the owner revokes the client in the Hermesh app or the console)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paired := a.load() == nil
			removed, err := a.forget()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if a.output == "json" {
				var id *authIdentity
				if paired {
					id, _ = a.identityAndGrant()
				}
				return a.report(w, nil, field{"logout", object{{"removed", removed}, {"identity", id}}})
			}
			switch {
			case !removed:
				fmt.Fprintln(w, "Not paired: nothing to forget.")
			case paired:
				fmt.Fprintf(w, "Forgot this client's key and grant. Owner %s still lists client %q (%s) until they revoke it in the Hermesh app or the console.\n",
					a.st.OwnerID, a.g.ClientLabel, a.g.ClientId)
			default:
				fmt.Fprintf(w, "Removed the pairing files in %s.\n", a.home)
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// auth status

// authCheck is one step of `auth status`.
type authCheck struct {
	Check string `json:"check"`
	// ok, warn, fail or skipped.
	Status string `json:"status"`
	Detail string `json:"detail"`
	// What to do about it (warn and fail only).
	Hint string `json:"hint"`
	code int
}

func (c *authCheck) set(status, detail, hint string, code int) {
	c.Status, c.Detail, c.Hint, c.code = status, detail, hint, code
}

// authReport is the `auth` object of the output contract.
type authReport struct {
	Identity *authIdentity `json:"identity"`
	Grant    *authGrant    `json:"grant"`
	Checks   []*authCheck  `json:"checks"`
	Home     string        `json:"home"`
	Version  string        `json:"version"`
}

type authIdentity struct {
	Client         string `json:"client"`
	ClientID       string `json:"client_id"`
	OwnerID        string `json:"owner_id"`
	Relay          string `json:"relay"`
	ConnectAddress string `json:"connect_address"`
}

type authGrant struct {
	Version uint64 `json:"version"`
	// "*" means all.
	Machines    []string `json:"machines"`
	Projects    []string `json:"projects"`
	Agents      []string `json:"agents"`
	Visibility  string   `json:"visibility"`
	MaxSessions uint32   `json:"max_sessions"`
	ExpiresAt   *string  `json:"expires_at"`
}

// identityAndGrant are this client's identity and grant as the contract shows them.
func (a *app) identityAndGrant() (*authIdentity, *authGrant) {
	g := a.g
	return &authIdentity{Client: g.ClientLabel, ClientID: g.ClientId, OwnerID: a.st.OwnerID, Relay: a.st.RelayURL, ConnectAddress: a.address()},
		&authGrant{Version: g.Version, Machines: nonNil(g.MachineIds), Projects: nonNil(g.ProjectIds),
			Agents: nonNil(g.Agents), Visibility: visibilityName(g.TaskVisibility), MaxSessions: g.MaxConcurrentSessions, ExpiresAt: tsJSON(g.ExpiresAt)}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (a *app) authStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check this client's pairing: identity, grant, and the relay connection",
		Long: `Check this client's pairing, step by step: the identity stored on this
computer, the owner's grant, the relay connection, and whether the grant is
the owner's latest. Each failing step says what to do. Exit code 0 when the
agent can work (warnings included), 3 when it is not paired, revoked or its
grant expired, 4 when the relay is unreachable.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.printAuthStatus(cmd.OutOrStdout(), a.authStatus(cmd.Context()))
		},
	}
}

func (a *app) authStatus(ctx context.Context) *authReport {
	id, grant, relay, sync := &authCheck{Check: "identity"}, &authCheck{Check: "grant"}, &authCheck{Check: "relay"}, &authCheck{Check: "grant_sync"}
	r := &authReport{Version: machine.Version, Home: a.home, Checks: []*authCheck{id, grant, relay, sync}}
	skip := func(why string, cs ...*authCheck) {
		for _, c := range cs {
			c.set("skipped", why, "", 0)
		}
	}
	err := a.load()
	if errors.Is(err, errNotLoggedIn) {
		id.set("fail", "not paired", "run `hermesh auth login` (the official center; or give a center URL or connection address) and have the owner approve the request it shows on their Mac", exitDenied)
		skip("not paired", grant, relay, sync)
		return r
	}
	if err != nil {
		id.set("fail", fmt.Sprintf("cannot read the pairing stored in %s: %v", a.home, err), "run `hermesh auth logout`, then `hermesh auth login <address>`", exitDenied)
		skip("no identity", grant, relay, sync)
		return r
	}
	relogin := fmt.Sprintf("run `hermesh auth login %s` to ask the owner again", a.address())
	id.set("ok", fmt.Sprintf("client %q (%s), owner %s", a.g.ClientLabel, a.g.ClientId, a.st.OwnerID), "", 0)
	if fi, err := os.Stat(a.keyPath()); a.mem == nil && err == nil && fi.Mode().Perm()&0o077 != 0 {
		id.set("warn", id.Detail+"; the key file is readable by others", fmt.Sprintf("run `chmod 600 %s`", a.keyPath()), 0)
	}

	// The relay may hold a newer grant, so it is asked before the grant is judged.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// refused: why the grant no longer works, when the relay says so.
	var refused string
	began := time.Now()
	c, err := a.dial(ctx, false)
	switch {
	case errors.Is(err, transport.ErrAdmission):
		relay.set("ok", fmt.Sprintf("%s · %d ms", a.st.RelayURL, time.Since(began).Milliseconds()), "", 0)
		refused = "refused by the relay: " + refusal(err)
		skip("the relay refused this client", sync)
	case err != nil:
		relay.set("fail", fmt.Sprintf("%s is unreachable: %v", a.st.RelayURL, err),
			"check the network; if the owner moved the relay, run `hermesh auth login <new address>`", exitUnavailable)
		skip("relay unreachable", sync)
	default:
		relay.set("ok", fmt.Sprintf("%s · %d ms", a.st.RelayURL, time.Since(began).Milliseconds()), "", 0)
		from, err := a.syncGrant(ctx, c)
		c.Close()
		switch {
		case errors.Is(err, errRevoked):
			refused = "revoked by the owner"
			sync.set("fail", refused, "", exitDenied)
		case err != nil:
			sync.set("warn", fmt.Sprintf("could not check for a newer grant: %v", err), "", 0)
		case from != 0:
			sync.set("ok", fmt.Sprintf("updated v%d → v%d", from, a.g.Version), "", 0)
		default:
			sync.set("ok", "up to date", "", 0)
		}
	}

	g, now := a.g, time.Now()
	r.Identity, r.Grant = a.identityAndGrant()
	summary := grantSummary(g)
	_, _, verr := a.trust.VerifyGrant(a.grant, now)
	switch left := g.ExpiresAt.AsTime().Sub(now); {
	case refused != "":
		grant.set("fail", summary+" · "+refused, relogin, exitDenied)
	case left <= 0:
		grant.set("fail", fmt.Sprintf("%s · expired %s ago", summary, approx(-left)), relogin, exitDenied)
	case g.NotBefore.AsTime().After(now):
		grant.set("fail", fmt.Sprintf("%s · not valid until %s", summary, g.NotBefore.AsTime().Local().Format(time.DateTime)),
			"check this computer's clock", exitDenied)
	case verr != nil:
		grant.set("fail", fmt.Sprintf("%s · invalid: %v", summary, verr), relogin, exitDenied)
	case left < 7*24*time.Hour:
		grant.set("warn", fmt.Sprintf("%s · expires in %s", summary, approx(left)), "ask the owner to renew it in the Hermesh app", 0)
	default:
		grant.set("ok", fmt.Sprintf("%s · expires in %s", summary, approx(left)), "", 0)
	}
	return r
}

// refusal is why the relay refused this client ("grant revoked").
func refusal(err error) string {
	msg := err.Error()
	var ee *exitError
	if errors.As(err, &ee) {
		msg = ee.err.Error()
	}
	if _, why, ok := strings.Cut(msg, transport.ErrAdmission.Error()+": "); ok {
		msg = why
	}
	return msg
}

// grantSummary is a grant in words: "v3 · full (...) · all machines, ...".
func grantSummary(g *identityv1.Grant) string {
	machines, projects, agents := fmt.Sprintf("%d machines", len(g.MachineIds)), "projects "+strings.Join(g.ProjectIds, ", "), strings.Join(g.Agents, ", ")
	if slices.Contains(g.MachineIds, "*") {
		machines = "all machines"
	}
	if slices.Contains(g.ProjectIds, "*") {
		projects = "all projects"
	}
	if slices.Contains(g.Agents, "*") {
		agents = "all agents"
	}
	parts := []string{fmt.Sprintf("v%d", g.Version), machines + ", " + projects, agents,
		"sees " + visibilityName(g.TaskVisibility) + " sessions"}
	if g.MaxConcurrentSessions > 0 {
		parts = append(parts, fmt.Sprintf("at most %d sessions", g.MaxConcurrentSessions))
	}
	return strings.Join(parts, " · ")
}

func visibilityName(v identityv1.TaskVisibility) string {
	if v == identityv1.TaskVisibility_TASK_VISIBILITY_ALL {
		return "all"
	}
	return "own"
}

// approx is a duration in rough words: "3 days", "5 hours".
func approx(d time.Duration) string {
	unit := func(n int, s string) string {
		if n == 1 {
			return "1 " + s
		}
		return fmt.Sprintf("%d %ss", n, s)
	}
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return unit(int(d.Minutes()+0.5), "minute")
	case d < 48*time.Hour:
		return unit(int(d.Hours()+0.5), "hour")
	}
	return unit(int(d.Hours()/24+0.5), "day")
}

// printAuthStatus prints the report and turns its first failure into the
// exit code.
func (a *app) printAuthStatus(w io.Writer, r *authReport) error {
	var err error
	for _, c := range r.Checks {
		if c.Status == "fail" {
			kind := map[string]string{"identity": "not_paired", "grant": "revoked", "relay": "unreachable", "grant_sync": "revoked"}[c.Check]
			err = &exitError{code: c.code, kind: kind, err: fmt.Errorf("%s: %s", c.Check, c.Detail), hint: c.Hint}
			break
		}
	}
	if a.output == "json" {
		return a.report(w, err, field{"auth", r})
	}
	fmt.Fprintf(w, "hermesh %s · config %s\n\n", r.Version, r.Home)
	marks := map[string]string{"ok": "✓", "warn": "!", "fail": "✗", "skipped": "-"}
	for _, c := range r.Checks {
		detail := c.Detail
		if c.Status == "skipped" {
			detail = "skipped (" + detail + ")"
		}
		fmt.Fprintf(w, "%s %-11s %s\n", marks[c.Status], strings.ReplaceAll(c.Check, "_", " "), detail)
		if c.Hint != "" {
			fmt.Fprintf(w, "  %-11s → %s\n", "", c.Hint)
		}
	}
	if err != nil {
		return reported(err) // the checks above say it
	}
	return nil
}
