package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	localv1 "github.com/hermeshai/hermesh/gen/go/hermesh/local/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/local/v1/localv1connect"
	"github.com/hermeshai/hermesh/internal/connaddr"
	"github.com/hermeshai/hermesh/internal/devicekey"
	"github.com/hermeshai/hermesh/internal/machine"
	"github.com/hermeshai/hermesh/internal/preview"
	"github.com/hermeshai/hermesh/internal/service"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// A Linux trusted device (docs/onboarding.md §9): the owner role runs as
// its own service (`hermeshd run --owner --no-machine`), never a machine;
// these commands talk to it and sign in this terminal, with the key sealed
// by a passphrase.

func deviceCommands(dataDir *string) []*cobra.Command {
	var noService bool
	device := &cobra.Command{Use: "device", Short: "This computer as one of your trusted devices, which approve (no coding agents run here)",
		Long: `A trusted device approves what asks to join your Hermesh, as your iPhone or
Mac does. Its key is sealed by a passphrase you choose. Use a computer, or
a user, that runs no coding agents: they could approve as you.`,
		Example: `  hermeshd device join       scan the code it shows with Hermesh on your iPhone
  hermeshd device setup      the first device, when you have no other
  hermeshd approve           approve what waits`}
	device.PersistentFlags().BoolVar(&noService, "no-service", false, "don't install the background service (run \"hermeshd run --owner --no-machine\" yourself)")

	join := &cobra.Command{
		Use:   "join [connection address | center URL]",
		Short: "Join your Hermesh as a trusted device: shows a code to scan with Hermesh on your iPhone or Mac",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var arg string
			if len(args) > 0 {
				arg = args[0]
			}
			c, err := ownerRole(cmd.Context(), *dataDir, noService)
			if err != nil {
				return err
			}
			t, err := newTerminal()
			if err != nil {
				return err
			}
			s := startSigner(cmd.Context(), c, *dataDir, t)
			defer s.stop()
			req := &localv1.RequestJoinRequest{RootLabel: hostname(), RootPlatform: "linux"}
			if relay, owner, err := connaddr.Parse(arg); err == nil && owner != "" {
				req.RelayUrl, req.OwnerId = relay, owner
			} else if req.RelayUrl, err = connaddr.Discover(cmd.Context(), http.DefaultClient, connaddr.Center(arg)); err != nil {
				return err
			}
			res, err := c.RequestJoin(cmd.Context(), req)
			if err != nil {
				return err
			}
			out := os.Stdout
			if res.VerificationCode != "" {
				fmt.Fprintf(out, "Verification code: %s\nApprove it in Hermesh on your iPhone or Mac if it shows this same code.\n", res.VerificationCode)
			} else {
				fmt.Fprintln(out, "\nJoin your Hermesh as a trusted device")
				side := []string{"Scan it with Hermesh on your iPhone,", "or enter the pairing code in Hermesh:", "", "  " + res.Code}
				if machine.IsTerminal(out) && res.PairingLink != "" {
					fmt.Fprint(out, machine.QRCode(res.PairingLink, side))
				} else {
					fmt.Fprintf(out, "Pairing code: %s\nEnter it in Hermesh on your iPhone or Mac.\n", res.Code)
				}
			}
			fmt.Fprintln(out, "Waiting...")
			for {
				w, err := c.WaitJoin(cmd.Context(), &localv1.WaitJoinRequest{Code: res.Code})
				if err != nil {
					return err
				}
				switch {
				case w.Expired:
					return errors.New("the request expired; run this again")
				case w.Claimed:
					who := ""
					if w.ClaimedBy != "" {
						who = " (" + w.ClaimedBy + ")"
					}
					fmt.Fprintf(out, "✓ Claimed%s: approve it in Hermesh on your iPhone or Mac\n", who)
				case w.Done:
					fmt.Fprintf(out, "✓ Approved on “%s”\n", w.ApprovedBy)
					for tries := 3; ; tries-- {
						digits, err := t.line("  Type the two digits it shows: ")
						if err != nil {
							return err
						}
						done, err := c.ConfirmJoin(cmd.Context(), &localv1.ConfirmJoinRequest{Code: res.Code, Accept: true, MatchCode: digits})
						if connect.CodeOf(err) == connect.CodeInvalidArgument && tries > 1 {
							fmt.Fprintf(out, "✗ Those aren’t the digits your device shows. %d tries left.\n", tries-1)
							continue
						}
						if err != nil {
							return err
						}
						fmt.Fprintf(out, "✓ “%s” is one of your trusted devices (owner %s)\nApprove what asks to join with: hermeshd approve\n", req.RootLabel, done.OwnerId)
						return nil
					}
				}
			}
		},
	}

	var claimCode string
	setup := &cobra.Command{
		Use:   "setup [center URL]",
		Short: "Set up your Hermesh with this computer as its first trusted device (signs in with a code)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var arg string
			if len(args) > 0 {
				arg = args[0]
			}
			t, err := newTerminal()
			if err != nil {
				return err
			}
			center := connaddr.Center(arg)
			relay, err := connaddr.Discover(cmd.Context(), http.DefaultClient, center)
			if err != nil {
				return err
			}
			req := &localv1.SetupRequest{RelayUrl: relay, ClaimCode: claimCode, RootLabel: hostname(), RootPlatform: "linux"}
			if claimCode == "" {
				// The browser sign-in hands back a link code, bound to this
				// challenge (docs/onboarding.md §9.5).
				b := make([]byte, 32)
				rand.Read(b)
				req.ClaimVerifier = base64.RawURLEncoding.EncodeToString(b)
				h := sha256.Sum256([]byte(req.ClaimVerifier))
				u := strings.TrimSuffix(center, "/") + "/app-login?cli=1&challenge=" + url.QueryEscape(base64.RawURLEncoding.EncodeToString(h[:]))
				fmt.Printf("Sign in to Hermesh in a browser, on any device:\n  %s\n", u)
				if req.ClaimCode, err = t.line("Then type the code it shows: "); err != nil {
					return err
				}
			}
			c, err := ownerRole(cmd.Context(), *dataDir, noService)
			if err != nil {
				return err
			}
			s := startSigner(cmd.Context(), c, *dataDir, t)
			defer s.stop()
			res, err := c.Setup(cmd.Context(), req)
			if err != nil {
				return err
			}
			fmt.Printf("✓ “%s” is your first trusted device (owner %s)\nAdd machines with: curl -fsSL https://github.com/hermeshai/hermesh/releases/latest/download/install.sh | sh -s -- %s\n",
				req.RootLabel, res.OwnerId, res.ConnectAddress)
			return nil
		},
	}
	setup.Flags().StringVar(&claimCode, "claim-code", "", "your own server's claim code, which it printed when it started")

	list := &cobra.Command{
		Use: "list", Short: "List your trusted devices", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := ownerRole(cmd.Context(), *dataDir, true)
			if err != nil {
				return err
			}
			res, err := c.ListDevices(cmd.Context(), &localv1.ListDevicesRequest{})
			if err != nil {
				return err
			}
			for _, d := range res.Devices {
				this := ""
				if d.ThisDevice {
					this = "  (this computer)"
				}
				fmt.Printf("%-24s %-8s %s%s\n", d.Label, d.Platform, d.KeyId[:12], this)
			}
			return nil
		},
	}

	remove := &cobra.Command{
		Use: "remove <name | key id>", Short: "Remove a lost trusted device; this one re-issues what it signed", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := newTerminal()
			if err != nil {
				return err
			}
			c, err := ownerRole(cmd.Context(), *dataDir, true)
			if err != nil {
				return err
			}
			res, err := c.ListDevices(cmd.Context(), &localv1.ListDevicesRequest{})
			if err != nil {
				return err
			}
			var found *localv1.Device
			for _, d := range res.Devices {
				if d.Label == args[0] || strings.HasPrefix(d.KeyId, args[0]) {
					found = d
				}
			}
			if found == nil {
				return fmt.Errorf("no trusted device %q (hermeshd device list)", args[0])
			}
			s := startSigner(cmd.Context(), c, *dataDir, t)
			defer s.stop()
			r, err := c.RemoveDevice(cmd.Context(), &localv1.RemoveDeviceRequest{KeyId: found.KeyId})
			if err != nil {
				return err
			}
			fmt.Printf("✓ Removed “%s”; re-issued %d machines and %d grants it signed\n", found.Label, r.ReissuedMachines, r.ReissuedGrants)
			return nil
		},
	}

	leave := &cobra.Command{
		Use: "leave", Short: "Stop being one of your trusted devices, and delete the key here", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := newTerminal()
			if err != nil {
				return err
			}
			c, err := ownerRole(cmd.Context(), *dataDir, true)
			if err != nil {
				return err
			}
			s := startSigner(cmd.Context(), c, *dataDir, t)
			defer s.stop()
			if _, err := c.Leave(cmd.Context(), &localv1.LeaveRequest{}); err != nil {
				return err
			}
			fmt.Println("✓ This computer is no longer one of your trusted devices")
			return nil
		},
	}
	device.AddCommand(join, setup, list, remove, leave)

	approve := &cobra.Command{
		Use:   "approve [pairing code]",
		Short: "Approve what asks to join your Hermesh: machines, agents and devices (on a trusted device)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := newTerminal()
			if err != nil {
				return err
			}
			c, err := ownerRole(cmd.Context(), *dataDir, true)
			if err != nil {
				return err
			}
			var waiting []*localv1.PairingDetails
			if len(args) > 0 {
				res, err := c.LookupPairing(cmd.Context(), &localv1.LookupPairingRequest{Code: args[0]})
				if err != nil {
					return err
				}
				waiting = append(waiting, res.Details)
			} else {
				res, err := c.ListPairingRequests(cmd.Context(), &localv1.ListPairingRequestsRequest{})
				if err != nil {
					return err
				}
				waiting = res.Pairings
			}
			if len(waiting) == 0 {
				fmt.Println("Nothing waits for approval. A machine's or a device's code: hermeshd approve <code>")
				return nil
			}
			s := startSigner(cmd.Context(), c, *dataDir, t)
			defer s.stop()
			for _, p := range waiting {
				if err := approveOne(cmd.Context(), c, t, p); err != nil {
					fmt.Fprintf(os.Stderr, "✗ %v\n", err)
				}
			}
			return nil
		},
	}
	return []*cobra.Command{device, approve}
}

func approveOne(ctx context.Context, c localv1connect.LocalServiceClient, t *terminal, p *localv1.PairingDetails) error {
	switch p.Kind.String() {
	case "PAIRING_KIND_DEVICE":
		fmt.Printf("\n“%s” wants to become your trusted device\n", p.Label)
	case "PAIRING_KIND_MACHINE":
		fmt.Printf("\nA machine calling itself “%s” wants to join\n", p.Label)
	default:
		fmt.Printf("\n“%s” wants to connect to your computers\n", p.Label)
	}
	if who := strings.Trim(p.Hostname+" · "+p.Platform, " ·"); who != "" {
		fmt.Printf("  It says it is: %s\n", who)
	}
	if !p.Confirmed {
		fmt.Printf("  Verification code: %s — approve only if it shows this same code\n", p.VerificationCode)
	}
	name := p.Label
	switch p.Kind.String() {
	case "PAIRING_KIND_MACHINE":
		n, err := t.line(fmt.Sprintf("  Name it [%s]: ", p.Label))
		if err != nil {
			return err
		}
		if n != "" {
			name = n
		}
	case "PAIRING_KIND_CLIENT":
		fmt.Println("  It can use every project, machine and coding agent, for 30 days. Change that later in Hermesh on your iPhone or Mac.")
	default:
		fmt.Println("  It will approve machines, agents and new devices, like this one. Approve only if that device is in your hands.")
	}
	ok, err := t.line("  Approve? [y/N] ")
	if err != nil {
		return err
	}
	if !strings.EqualFold(ok, "y") && !strings.EqualFold(ok, "yes") {
		fmt.Println("  Not approved.")
		return nil
	}
	switch p.Kind.String() {
	case "PAIRING_KIND_DEVICE":
		res, err := c.ApproveDevicePairing(ctx, &localv1.ApproveDevicePairingRequest{Code: p.Code})
		if err != nil {
			return err
		}
		fmt.Printf("✓ Approved. Type these digits on “%s”: %s\n", p.Label, res.MatchCode)
	case "PAIRING_KIND_MACHINE":
		res, err := c.ApproveMachinePairing(ctx, &localv1.ApproveMachinePairingRequest{Code: p.Code, MachineName: name})
		if err != nil {
			return err
		}
		if res.MatchCode != "" {
			fmt.Printf("✓ Approved. Type these digits on “%s”: %s\n", name, res.MatchCode)
		} else {
			fmt.Printf("✓ Approved “%s”\n", name)
		}
	default:
		if _, err := c.ApproveClientPairing(ctx, &localv1.ApproveClientPairingRequest{Code: p.Code,
			Spec: &localv1.GrantSpec{Label: p.Label, MachineIds: []string{"*"}, ProjectIds: []string{"*"}, Agents: []string{"*"}, TtlDays: 30}}); err != nil {
			return err
		}
		fmt.Printf("✓ “%s” is connected\n", p.Label)
	}
	return nil
}

// ownerRole is a client of this user's owner role, started as a service
// first unless it runs (or noService). A user whose hermeshd is a machine
// can't be a trusted device: its coding agents could approve as the owner
// (docs/onboarding.md §9.2).
func ownerRole(ctx context.Context, dataDir string, noService bool) (localv1connect.LocalServiceClient, error) {
	if _, err := os.Stat(machine.KeyPath(filepath.Join(dataDir, "machine"))); err == nil {
		if n, err := machine.Open(machine.Config{DataDir: filepath.Join(dataDir, "machine")}); err != nil || n.Paired() {
			if n != nil {
				n.Close()
			}
			return nil, errors.New("this user's Hermesh is a machine: its coding agents could approve as you. Use another user or computer as a trusted device")
		} else {
			n.Close()
		}
	}
	sock := SocketPath(dataDir)
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	c := localv1connect.NewLocalServiceClient(hc, "http://local")
	up := func() bool {
		_, err := c.GetStatus(ctx, &localv1.GetStatusRequest{})
		return err == nil
	}
	if up() {
		return c, nil
	}
	if noService {
		return nil, fmt.Errorf("the owner role isn't running: start it with `hermeshd run --owner --no-machine --data-dir %s`", dataDir)
	}
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if err := service.Install(bin, dataDir, filepath.Join(dataDir, "logs"), "--owner", "--no-machine"); err != nil {
		return nil, fmt.Errorf("the background service: %w", err)
	}
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if up() {
			return c, nil
		}
	}
	return nil, errors.New("the background service didn't start; see `journalctl --user -u hermeshd`")
}

func hostname() string {
	h, _ := os.Hostname()
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}

// terminal is where a person answers: stdin when it is one, else /dev/tty
// (under `curl | sh`).
type terminal struct {
	mu  sync.Mutex
	in  *os.File
	r   *bufio.Reader
	out io.Writer
}

func newTerminal() (*terminal, error) {
	in := os.Stdin
	if !term.IsTerminal(int(in.Fd())) {
		tty, err := os.Open("/dev/tty")
		if err != nil {
			return nil, errors.New("this needs a terminal to answer in")
		}
		in = tty
	}
	return &terminal{in: in, r: bufio.NewReader(in), out: os.Stdout}, nil
}

func (t *terminal) line(prompt string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprint(t.out, prompt)
	l, err := t.r.ReadString('\n')
	if err != nil && l == "" {
		return "", err
	}
	return strings.TrimSpace(l), nil
}

func (t *terminal) secret(prompt string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprint(t.out, prompt)
	b, err := term.ReadPassword(int(t.in.Fd()))
	fmt.Fprintln(t.out)
	return string(b), err
}

// signer answers the owner role's signing requests while a command runs:
// it shows what each batch signs (internal/preview), asks for the
// passphrase, and signs with the key it opens; the key stays open only
// until the command ends.
type signer struct {
	c      localv1connect.LocalServiceClient
	dir    string
	t      *terminal
	cancel context.CancelFunc
	done   chan struct{}
	keys   map[string]*identity.SoftwareKey
}

func startSigner(ctx context.Context, c localv1connect.LocalServiceClient, dataDir string, t *terminal) *signer {
	ctx, cancel := context.WithCancel(ctx)
	s := &signer{c: c, dir: filepath.Join(dataDir, "owner-keys"), t: t, cancel: cancel, done: make(chan struct{}), keys: map[string]*identity.SoftwareKey{}}
	reqs := make(chan *localv1.SigningRequest, 16)
	go func() {
		defer close(reqs)
		for ctx.Err() == nil {
			stream, err := c.WatchSigningRequests(ctx, &localv1.WatchSigningRequestsRequest{})
			if err != nil {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			for stream.Receive() {
				reqs <- stream.Msg()
			}
		}
	}()
	go func() {
		defer close(s.done)
		var next *localv1.SigningRequest
		for {
			r := next
			if next = nil; r == nil {
				var ok bool
				if r, ok = <-reqs; !ok {
					return
				}
			}
			// The rest of its batch comes right after it.
			batch := []*localv1.SigningRequest{r}
		gather:
			for r.BatchId != "" {
				select {
				case n, ok := <-reqs:
					if !ok {
						break gather
					}
					if n.BatchId != r.BatchId {
						next = n
						break gather
					}
					batch = append(batch, n)
				case <-time.After(300 * time.Millisecond):
					break gather
				}
			}
			s.handle(ctx, batch)
		}
	}()
	return s
}

func (s *signer) stop() {
	s.cancel()
	<-s.done
}

func (s *signer) handle(ctx context.Context, batch []*localv1.SigningRequest) {
	if len(batch) == 0 {
		return
	}
	first := batch[0]
	reject := func(why string) {
		fmt.Fprintf(os.Stderr, "✗ Not signed: %s\n", why)
		for _, r := range batch {
			s.c.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: r.RequestId, Rejected: true})
		}
	}
	switch {
	case first.CreateRootKey:
		for {
			p1, err := s.t.secret("Choose a passphrase for this device's key: ")
			if err != nil {
				reject(err.Error())
				return
			}
			if len(p1) < 8 {
				fmt.Println("  At least 8 characters.")
				continue
			}
			p2, err := s.t.secret("Type it again: ")
			if err != nil {
				reject(err.Error())
				return
			}
			if p1 != p2 {
				fmt.Println("  They don't match.")
				continue
			}
			k, err := devicekey.Create(s.dir, p1)
			if err != nil {
				reject(err.Error())
				return
			}
			s.keys[identity.KeyID(k.Public())] = k
			s.c.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: first.RequestId, PublicKey: identity.MarshalPublicKey(k.Public())})
			return
		}
	case first.DeleteRootKey:
		devicekey.Delete(s.dir, first.RootKeyId)
		delete(s.keys, first.RootKeyId)
		s.c.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: first.RequestId})
		return
	}
	h := s.hints(ctx)
	cards, err := preview.Cards(batch, h)
	if err != nil {
		reject(err.Error())
		return
	}
	for _, c := range cards {
		fmt.Printf("\n  %s\n", c.Title)
		if c.Code != "" {
			fmt.Printf("  Verification code: %s\n", c.Code)
		}
		for _, l := range c.Lines {
			fmt.Printf("    %s\n", l)
		}
		if c.Why != "" {
			fmt.Printf("  %s\n", c.Why)
		}
		if c.Warning != "" {
			fmt.Printf("  ! %s\n", c.Warning)
		}
	}
	k := s.keys[first.RootKeyId]
	for tries := 3; k == nil; tries-- {
		p, err := s.t.secret("\nPassphrase to sign (Enter to refuse): ")
		if err != nil || p == "" {
			reject("refused")
			return
		}
		if k, err = devicekey.Open(s.dir, first.RootKeyId, p); errors.Is(err, devicekey.ErrPassphrase) && tries > 1 {
			fmt.Println("  Wrong passphrase.")
			continue
		} else if err != nil {
			reject(err.Error())
			return
		}
		s.keys[first.RootKeyId] = k
	}
	for _, r := range batch {
		sig, err := k.SignMessage(r.Message)
		if err != nil {
			reject(err.Error())
			return
		}
		s.c.CompleteSigningRequest(ctx, &localv1.CompleteSigningRequestRequest{RequestId: r.RequestId, Signature: sig})
	}
}

// hints are names from the owner role's lists, for the cards.
func (s *signer) hints(ctx context.Context) preview.Hints {
	h := preview.Hints{MachineNames: map[string]string{}}
	if res, err := s.c.ListMachines(ctx, &localv1.ListMachinesRequest{}); err == nil {
		for _, m := range res.Machines {
			h.MachineNames[m.MachineId] = m.MachineName
		}
	}
	if res, err := s.c.ListDevices(ctx, &localv1.ListDevicesRequest{}); err == nil {
		h.Devices = map[string]string{}
		for _, d := range res.Devices {
			h.Devices[d.KeyId] = d.Label
		}
	}
	return h
}
