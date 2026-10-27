package machine

import (
	"io"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"

	"google.golang.org/protobuf/proto"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

// Views (AIP-157, docs/state-sync.md §3.1): BASIC is what lists and
// conversations show, FULL what callers that predate views get.

const (
	// A tool result's preview: its first previewLines lines, at most
	// previewBytes bytes.
	previewLines = 3
	previewBytes = 512
	// inlineOutput bytes of a tool's output stay in its event (FULL); a
	// longer output is kept whole beside it (tool_outputs).
	inlineOutput = 16 << 10
)

// BasicSession is s as lists show it: without result,
// progress.last_message_excerpt and provider_session_ref.
func BasicSession(s *sessionv1.Session) *sessionv1.Session {
	if s.Result == "" && s.ProviderSessionRef == "" && s.GetProgress().GetLastMessageExcerpt() == "" {
		return s
	}
	b := proto.CloneOf(s)
	b.Result, b.ProviderSessionRef = "", ""
	if b.Progress != nil {
		b.Progress.LastMessageExcerpt = ""
	}
	return b
}

// BasicSessions is BasicSession of each.
func BasicSessions(list []*sessionv1.Session) []*sessionv1.Session {
	out := make([]*sessionv1.Session, len(list))
	for i, s := range list {
		out[i] = BasicSession(s)
	}
	return out
}

// BasicEvent is e as conversations show it: a tool result longer than its
// preview cut to it, with preview and its whole size set.
func BasicEvent(e *sessionv1.SessionEvent) *sessionv1.SessionEvent {
	r := e.GetToolResult()
	if r == nil || r.Preview {
		return e
	}
	p := Preview(r.Summary)
	if len(p) == len(r.Summary) {
		return e
	}
	b := proto.CloneOf(e)
	br := b.GetToolResult()
	br.Summary, br.Preview = p, true
	if br.Size == 0 {
		br.Size = uint64(len(r.Summary))
	}
	return b
}

// Preview is s's first previewLines lines, at most previewBytes bytes, cut
// at a UTF-8 boundary.
func Preview(s string) string {
	end := len(s)
	for i, n := 0, 0; i < len(s); i++ {
		if s[i] == '\n' {
			if n++; n == previewLines && i < len(s)-1 { // three lines and more
				end = i
				break
			}
		}
	}
	if end > previewBytes {
		end = previewBytes
		for end > 0 && !utf8.RuneStart(s[end]) {
			end--
		}
	}
	return s[:end]
}

// outputChunk bytes at most go in one ToolOutputChunk.
const outputChunk = 64 << 10

// ReadToolOutput sends a tool's whole output, as recorded, in chunks: from
// beside its event when it is longer than its event holds, else from its
// event. The first chunk goes before the rest is read.
func (s *Sessions) ReadToolOutput(p *Principal, req *sessionv1.ReadToolOutputRequest, send func(*sessionv1.ToolOutputChunk) error) error {
	if _, _, err := s.find(p, req.SessionId, false); err != nil {
		return err
	}
	size, out, ok, err := s.st.ToolOutput(req.SessionId, req.CallId)
	if err != nil {
		return err
	}
	if !ok {
		for after := uint64(0); out == nil; {
			evs, err := s.st.Events(req.SessionId, after, 500)
			if err != nil {
				return err
			}
			if len(evs) == 0 {
				return cerr(connect.CodeNotFound, "no output of %s in session %s", req.CallId, req.SessionId)
			}
			for _, e := range evs {
				if r := e.GetToolResult(); r != nil && r.CallId == req.CallId {
					size, out = uint64(len(r.Summary)), strings.NewReader(r.Summary)
				}
			}
			after = evs[len(evs)-1].Seq
		}
	}
	first := &sessionv1.ToolOutputChunk{Size: size}
	for {
		data := make([]byte, outputChunk)
		n, err := io.ReadFull(out, data)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		if n == 0 && first == nil {
			return nil
		}
		c := first
		if c == nil {
			c = &sessionv1.ToolOutputChunk{}
		}
		first = nil
		c.Data = data[:n]
		if err := send(c); err != nil {
			return err
		}
		if n < outputChunk {
			return nil
		}
	}
}
