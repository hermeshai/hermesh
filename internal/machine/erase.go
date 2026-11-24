package machine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1/relayv1connect"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Erasing everything (docs/erase-everything.md): an owner's signed order,
// which the relay keeps after it forgot the owner. A machine, device or
// agent that is refused fetches it, verifies it against the root chain it
// holds, says it is erasing, and erases what Hermesh keeps there.

// ErrErased is what Run returns once the owner's erase order came: the
// machine is then to erase its data (Erase) and stop.
var ErrErased = errors.New("the owner erased everything")

func FetchEraseOrder(ctx context.Context, hc connect.HTTPClient, relayURL, ownerID string, trust *identity.Trust) *identityv1.EraseOrder {
	if trust == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	info, err := relayv1connect.NewRelayPublicServiceClient(hc, strings.TrimSuffix(relayURL, "/")).GetInfo(ctx, &relayv1.GetInfoRequest{OwnerId: ownerID})
	if err != nil || info.EraseOrder == nil {
		return nil
	}
	trust.AcceptRootChain(info.RootChain)
	order, err := trust.VerifyEraseOrder(info.EraseOrder)
	if err != nil {
		return nil
	}
	return order
}

func AckErase(ctx context.Context, hc connect.HTTPClient, relayURL, eraseID, subject string, deviceCert *identityv1.Signed, key identity.Signer) error {
	ack, err := identity.SignEraseAck(eraseID, subject, deviceCert, key)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err = relayv1connect.NewRelayPublicServiceClient(hc, strings.TrimSuffix(relayURL, "/")).AckErase(ctx, &relayv1.AckEraseRequest{Ack: ack})
	return err
}

func (n *Machine) checkErase(ctx context.Context) bool {
	n.mu.Lock()
	relayURL, ownerID, trust, said := n.relayURL, n.ownerID, n.trust, n.goneSaid
	n.mu.Unlock()
	order := FetchEraseOrder(ctx, n.cfg.HTTPClient, relayURL, ownerID, trust)
	if order == nil {
		if !said && OwnerGone(ctx, n.cfg.HTTPClient, relayURL, ownerID) {
			n.mu.Lock()
			n.goneSaid = true
			n.mu.Unlock()
			n.log.Warn("the owner is gone from its server (it erased everything, or was reset): pair this machine again with `hermeshd pair <connection address>`")
		}
		return false
	}
	if err := AckErase(ctx, n.cfg.HTTPClient, relayURL, order.EraseId, n.machineID, nil, n.key); err != nil {
		n.log.Warn("telling the owner this machine is erasing", "err", err)
	}
	n.audit.Log(AuditRecord{Kind: "erased", Subject: order.EraseId, Detail: "the owner erased everything"})
	n.log.Warn("the owner erased everything: erasing this machine", "erase", order.EraseId)
	return true
}

// Erase erases what Hermesh keeps for the machine in cfg.DataDir (which
// must not be running): its key, owner, projects list and sessions, and
// the worktrees it made, whose branches stay in their repositories. A
// worktree with uncommitted changes or local files beyond its checkpoint is
// kept, moved into keepDir when it can be; Erase returns where each is.
func Erase(cfg Config, keepDir string) ([]string, error) {
	n, err := Open(cfg)
	if err != nil {
		return nil, err
	}
	var kept []string
	sessions, err := n.sessions.ListAllSessions(true)
	if err != nil {
		n.Close()
		return nil, err
	}
	ctx := context.Background()
	for _, s := range sessions {
		wt, ok := sessionUsesWorktree(s)
		if !ok {
			wt = n.expectedWorktreePath(s.SessionId)
		}
		if st, sErr := os.Stat(wt); sErr != nil || !st.IsDir() {
			continue
		}
		repo := n.repoPathForProject(s.ProjectId)
		if repo == "" || checkSessionCheckout(ctx, n.sessions, s, repo, wt) != nil {
			dst := filepath.Join(keepDir, s.SessionId)
			if repo != "" && os.MkdirAll(keepDir, 0o700) == nil {
				if err := moveWorktree(ctx, repo, wt, dst); err == nil {
					wt = dst
				} else {
					n.log.Warn("keeping a worktree where it is", "path", wt, "err", err)
				}
			}
			kept = append(kept, wt)
			continue
		}
		_, _ = gitUnlimited(ctx, repo, "worktree", "remove", "--force", "--", filepath.ToSlash(wt))
		_ = os.RemoveAll(wt)
	}
	n.Close()
	return kept, RemoveExcept(cfg.DataDir, append(slices.Clone(kept), keepDir))
}

func moveWorktree(ctx context.Context, repo, from, to string) error {
	name := filepath.ToSlash(from)
	if b, err := os.ReadFile(filepath.Join(from, ".git")); err == nil {
		admin := filepath.FromSlash(strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:")))
		if g, err := os.ReadFile(filepath.Join(admin, "gitdir")); err == nil {
			if p, ok := strings.CutSuffix(strings.TrimSpace(string(g)), "/.git"); ok {
				name = p
			}
		}
	}
	_, err := git(ctx, repo, "worktree", "move", name, to)
	return err
}

func RemoveExcept(dir string, keep []string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		switch {
		case slices.Contains(keep, path):
		case slices.ContainsFunc(keep, func(k string) bool { return strings.HasPrefix(k, path+string(filepath.Separator)) }):
			if err := RemoveExcept(path, keep); err != nil {
				return err
			}
		default:
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
	}
	return nil
}
