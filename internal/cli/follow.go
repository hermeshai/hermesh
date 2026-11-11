package cli

import (
	"context"
	"errors"
	"net/http"

	inboxv1 "github.com/hermeshai/hermesh/gen/go/hermesh/inbox/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/pkg/identity"
)

// Follow takes the messages of opts.Identity's inbox for a caller that
// holds an agent's credentials and takes its messages itself (hermeshd for
// Her, docs/her-mesh.md §6.1): those that waited, then each as it arrives.
// take says what became of a message ("handled", "held" or "refused"; ""
// leaves it waiting), and the post office is told. A message that doesn't
// open or check is refused. Follow returns when the connection fails or
// ctx ends.
func Follow(ctx context.Context, opts ExecOptions, take func(m *inboxv1.Message, from *identity.ContactInfo) string) error {
	a := &app{hc: opts.HTTPClient, mem: &opts}
	if a.hc == nil {
		a.hc = http.DefaultClient
	}
	c, _, err := a.connect(ctx, nil)
	if err != nil {
		return err
	}
	defer c.Close()
	ms, err := a.messages(c)
	if err != nil {
		return err
	}
	priv, err := a.inboxKey()
	if err != nil {
		return err
	}
	for {
		left := 0
		for {
			res, err := ms.Fetch(ctx, &relayv1.FetchMessagesRequest{Skip: uint32(left)})
			if err != nil {
				return err
			}
			req := &relayv1.AckMessagesRequest{}
			for _, sm := range res.Messages {
				outcome := "refused"
				if m, from, err := openMessage(a.trust, priv, a.g.ClientId, sm); err == nil {
					outcome = take(m, from)
				}
				if outcome == "" {
					left++
					continue
				}
				req.Acks = append(req.Acks, &relayv1.MessageAck{MessageId: sm.MessageId, Outcome: outcome})
			}
			if len(req.Acks) > 0 {
				if _, err := ms.Ack(ctx, req); err != nil {
					return err
				}
			}
			if !res.More || len(res.Messages) == 0 {
				break
			}
		}
		if err := a.waitInbox(ctx, ms, left); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// ErrRevoked reports whether err from Follow says the owner revoked the
// identity (or its grant expired).
func ErrRevoked(err error) bool {
	var e *exitError
	return errors.As(err, &e) && e.kind == "revoked"
}
