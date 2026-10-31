package machine

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
)

func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.Set(0, 0, color.RGBA{0, 0x80, 0, 0xff})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Attached files are saved outside the worktree and listed in the prompt;
// images (only real ones) are also given to the agent as images.
func TestAttachmentsAreSavedAndShown(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	sess, err := h.s.Start(context.Background(), h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "fake", Prompt: "images",
		Attachments: []*sessionv1.Attachment{
			{Name: "shot.png", Data: pngBytes(t)},
			{Name: "../../notes.txt", Data: []byte("the secret is 42\n")},
			{Name: "fake.png", Data: []byte("not an image")},
			{Name: "notes.txt", Data: []byte("second notes")},
		}})
	if err != nil {
		t.Fatal(err)
	}
	id := sess.SessionId
	sess = h.wait(id, idle)
	if tc := h.lastTurn(id); tc.Result != "images: shot.png" {
		t.Fatalf("images the agent got: %v", tc)
	}
	var um *sessionv1.UserMessage
	for _, e := range h.events(id) {
		if u := e.GetUserMessage(); u != nil {
			um = u
		}
	}
	if len(um.Attachments) != 4 {
		t.Fatalf("attachments: %v", um.Attachments)
	}
	names := []string{"shot.png", "notes.txt", "fake.png", "notes-2.txt"}
	for i, f := range um.Attachments {
		if f.Name != names[i] || !strings.Contains(um.Text, f.Path) || f.Image != (i == 0) {
			t.Fatalf("attachment %d: %v\n%s", i, f, um.Text)
		}
		if !strings.HasPrefix(f.Path, h.s.attachmentsDir(id)+string(filepath.Separator)) {
			t.Fatalf("saved at %s", f.Path)
		}
	}
	if b, _ := os.ReadFile(um.Attachments[1].Path); string(b) != "the secret is 42\n" || um.Attachments[1].MediaType != "text/plain" {
		t.Fatalf("notes.txt: %q %s", b, um.Attachments[1].MediaType)
	}
	if !strings.Contains(um.Text, "shown as an image") || strings.Count(um.Text, "shown as an image") != 1 {
		t.Fatalf("prompt: %s", um.Text)
	}
	if d, err := h.s.Diff(context.Background(), h.p, &sessionv1.GetDiffRequest{SessionId: id}); err != nil || d.Diff != "" {
		t.Fatalf("attachments must not be in the worktree diff: %v %q", err, d.GetDiff())
	}

	// With a message, steered into a running turn, and with no text.
	h.send(id, "say one\nsleep 2s\nimages") // time enough for a slow runner to steer the file in
	h.said(id, "one")
	if _, err := h.s.Send(context.Background(), h.p, id, "", []*sessionv1.Attachment{{Name: "more.png", Data: pngBytes(t)}}); err != nil {
		t.Fatal(err)
	}
	h.wait(id, idle)
	if tc := h.lastTurn(id); tc.Turn != 2 || tc.Result != "images: more.png" {
		t.Fatalf("turn: %v\n%s", tc, h.dump(id))
	}
}

func TestAttachmentLimits(t *testing.T) {
	h := newHarness(t, DefaultLimits())
	many := make([]*sessionv1.Attachment, maxAttachments+1)
	for i := range many {
		many[i] = &sessionv1.Attachment{Name: "f", Data: []byte("x")}
	}
	big := []*sessionv1.Attachment{{Name: "big.bin", Data: make([]byte, maxAttachmentBytes+1)}}
	for _, atts := range [][]*sessionv1.Attachment{many, big} {
		_, err := h.s.Start(context.Background(), h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "fake", Prompt: "say x", Attachments: atts})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("got %v", err)
		}
	}
	if _, err := h.s.Start(context.Background(), h.p, &sessionv1.StartSessionRequest{ProjectId: "demo", Agent: "fake"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty start: %v", err)
	}
}

// A message joined from an agent's (recorded when it arrived) and the
// creator's own records only the creator's, once.
func TestJoinRecordsEachMessageOnce(t *testing.T) {
	letter := message{text: "[Hermesh] A message from helper.\n\nlook at the logs", quiet: true, answer: "look at the logs"}
	own := message{text: "and the tests"}
	for _, j := range []message{letter.join(own), own.join(letter)} {
		ev := j.userEvents()
		if len(ev) != 1 || ev[0].GetUserMessage().GetText() != "and the tests" {
			t.Fatalf("recorded: %v", ev)
		}
		if !strings.Contains(j.answerText(), "look at the logs") || !strings.Contains(j.answerText(), "and the tests") || strings.Contains(j.answerText(), "[Hermesh]") {
			t.Fatalf("its answer: %q", j.answerText())
		}
	}
	if ev := letter.join(letter).userEvents(); len(ev) != 0 {
		t.Fatalf("two quiet ones record nothing: %v", ev)
	}
	if ev := own.join(own).userEvents(); len(ev) != 1 || ev[0].GetUserMessage().GetText() != "and the tests\n\nand the tests" {
		t.Fatalf("two of the creator's: %v", ev)
	}
}
