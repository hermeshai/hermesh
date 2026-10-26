package machine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"connectrpc.com/connect"
	"github.com/oklog/ulid/v2"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/provider"
)

const (
	maxAttachments     = 20
	maxAttachmentBytes = 32 << 20
	// Larger images are only listed by path (Claude's API takes images up
	// to 5 MB base64-encoded).
	maxInlineImage = 3584 << 10
)

// inlineImage lists the image types agents take as image input.
var inlineImage = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// message is a user message as the agent gets it: the text (listing any
// attached files) and the saved files.
type message struct {
	text  string
	files []*sessionv1.AttachmentInfo
	// by is who it is from (Principal.ID): the asker of the turn it is in
	// (docs/agent-messages.md §7.2); conv is the asker's own conversation,
	// where the turn's news goes.
	by, conv string
	// quiet: an agent's message, recorded as MessageReceived when it
	// arrived; its turn records no UserMessage for it. answer is what of
	// it answers a question: its text without the words Hermesh puts
	// around it. events, for one joined from quiet and other messages,
	// are what its turn records of the others.
	quiet  bool
	answer string
	events []*sessionv1.SessionEvent
}

func (m message) input() provider.Input {
	in := provider.Input{Text: m.text}
	for _, f := range m.files {
		if f.Image {
			in.Images = append(in.Images, provider.Image{Path: f.Path, MediaType: f.MediaType})
		}
	}
	return in
}

func (m message) join(o message) message {
	if m.text == "" && len(m.files) == 0 {
		return o
	}
	j := message{text: m.text + "\n\n" + o.text, files: append(append([]*sessionv1.AttachmentInfo{}, m.files...), o.files...), by: m.by, conv: m.conv,
		quiet: m.quiet && o.quiet, answer: m.answerText() + "\n\n" + o.answerText()}
	if m.quiet != o.quiet || m.events != nil || o.events != nil {
		j.events = append(m.userEvents(), o.userEvents()...)
	}
	return j
}

func (m message) answerText() string {
	if m.answer != "" {
		return m.answer
	}
	return m.text
}

// userEvents are what the session records when the agent takes m.
func (m message) userEvents() []*sessionv1.SessionEvent {
	if m.events != nil {
		return m.events
	}
	if m.quiet {
		return nil
	}
	return []*sessionv1.SessionEvent{{Kind: &sessionv1.SessionEvent_UserMessage{UserMessage: &sessionv1.UserMessage{Text: m.text, Attachments: m.files}}}}
}

// checkAttachments validates a message's files before anything is saved.
func checkAttachments(text string, atts []*sessionv1.Attachment) error {
	if strings.TrimSpace(text) == "" && len(atts) == 0 {
		return cerr(connect.CodeInvalidArgument, "message is empty")
	}
	if len(atts) > maxAttachments {
		return cerr(connect.CodeInvalidArgument, "%d attachments; at most %d per message", len(atts), maxAttachments)
	}
	total := 0
	for _, a := range atts {
		total += len(a.Data)
	}
	if total > maxAttachmentBytes {
		return cerr(connect.CodeInvalidArgument, "attachments are %d MiB; at most %d MiB per message", total>>20, maxAttachmentBytes>>20)
	}
	return nil
}

// newMessage saves a message's files in a new directory under dir and
// lists them after the text.
func newMessage(dir, text string, atts []*sessionv1.Attachment) (message, error) {
	m := message{text: text}
	if len(atts) == 0 {
		return m, nil
	}
	dir = filepath.Join(dir, strings.ToLower(ulid.Make().String()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return m, err
	}
	var b strings.Builder
	b.WriteString("[Hermesh] Attached files, saved on this machine outside the worktree:\n")
	used := map[string]bool{}
	for _, a := range atts {
		name := uniqueName(fileName(a.Name), used)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, a.Data, 0o600); err != nil {
			return m, err
		}
		sum := sha256.Sum256(a.Data)
		mt := mediaType(name, a.MediaType, a.Data)
		// An image only when its content is what it claims: a mislabeled
		// file sent as an image would fail the agent's turn.
		img := inlineImage[mt] && len(a.Data) <= maxInlineImage && http.DetectContentType(a.Data) == mt
		m.files = append(m.files, &sessionv1.AttachmentInfo{Name: name, MediaType: mt, Size: uint64(len(a.Data)), Path: path, Sha256: hex.EncodeToString(sum[:]), Image: img})
		note := ""
		if img {
			note = ", shown as an image"
		}
		fmt.Fprintf(&b, "- %s (%s, %s%s)\n", path, mt, humanSize(len(a.Data)), note)
	}
	if strings.TrimSpace(text) == "" {
		m.text = strings.TrimSpace(b.String())
	} else {
		m.text = text + "\n\n" + strings.TrimSpace(b.String())
	}
	return m, nil
}

// fileName keeps the base name, without control characters.
func fileName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	if r := []rune(name); len(r) > 120 {
		ext := filepath.Ext(name)
		name = string(r[:120-len([]rune(ext))]) + ext
	}
	return name
}

func uniqueName(name string, used map[string]bool) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; used[strings.ToLower(name)]; i++ {
		name = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	used[strings.ToLower(name)] = true
	return name
}

func mediaType(name, given string, data []byte) string {
	t := given
	if t == "" {
		t = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	}
	if t == "" {
		t = http.DetectContentType(data)
	}
	if mt, _, err := mime.ParseMediaType(t); err == nil {
		return mt
	}
	return "application/octet-stream"
}

// filesDetail is the audit detail of attached files.
func filesDetail(files []*sessionv1.AttachmentInfo) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, " file=%s:%d:%s", f.Name, f.Size, f.Sha256)
	}
	return b.String()
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
