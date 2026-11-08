package herhost

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"connectrpc.com/connect"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
)

// noHerHere skips a test of what only a machine running her does (links,
// private folders) on Windows, where her does not run.
func noHerHere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("her does not run on Windows")
	}
}

// onAt is a Host as Her on would have it with her's home at home, without
// her (files need none).
func onAt(t *testing.T, home string) *Host {
	h := New(Config{})
	h.stop, h.home = func() {}, home
	return h
}

// herHomeWithBot is a her home holding the assistant b1.
func herHomeWithBot(t *testing.T) string {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "bots", "b1", "files"), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, "bots", "b1", "bot.json"), []byte(`{"id":"b1"}`), 0o600)
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("secret = 1\n"), 0o600)
	return home
}

func upload(h *Host, bot, name string, size int, data string) (string, error) {
	res, err := h.Upload(context.Background(), &herv1.UploadStart{BotId: bot, Name: name, Size: uint64(size)}, strings.NewReader(data))
	return res.GetPath(), err
}

func download(h *Host, bot, path string) (*herv1.DownloadStart, string, error) {
	var start *herv1.DownloadStart
	var data bytes.Buffer
	err := h.Download(context.Background(), &herv1.DownloadRequest{BotId: bot, Path: path}, func(r *herv1.DownloadResponse) error {
		if s := r.GetStart(); s != nil {
			start = s
		}
		data.Write(r.GetData())
		return nil
	})
	return start, data.String(), err
}

func code(t *testing.T, err error, want connect.Code) {
	t.Helper()
	if connect.CodeOf(err) != want {
		t.Fatalf("got %v, want %v", err, want)
	}
}

// An upload lands in the assistant's uploads/, a number added when the name
// is taken, only when its bytes add up; nothing but a file's name in an
// existing assistant's folder is taken.
func TestUpload(t *testing.T) {
	noHerHere(t)
	home := herHomeWithBot(t)
	h := onAt(t, home)
	uploads, _ := filepath.EvalSymlinks(filepath.Join(home, "bots", "b1", "files"))
	uploads = filepath.Join(uploads, "uploads")

	p, err := upload(h, "b1", "notes.txt", 5, "hello")
	if err != nil || p != filepath.Join(uploads, "notes.txt") {
		t.Fatalf("%s, %v", p, err)
	}
	if b, _ := os.ReadFile(p); string(b) != "hello" {
		t.Fatalf("%q", b)
	}
	if p, err = upload(h, "b1", "notes.txt", 2, "hi"); err != nil || p != filepath.Join(uploads, "notes-2.txt") {
		t.Fatalf("%s, %v", p, err)
	}

	for _, c := range []struct {
		bot, name string
		size      int
		data      string
		want      connect.Code
	}{
		{"b1", "short.txt", 9, "hello", connect.CodeInvalidArgument},
		{"b1", "long.txt", 2, "hello", connect.CodeInvalidArgument},
		{"b1", "", 0, "", connect.CodeInvalidArgument},
		{"b1", "..", 0, "", connect.CodeInvalidArgument},
		{"b1", "../escape.txt", 1, "x", connect.CodeInvalidArgument},
		{"b1", `..\escape.txt`, 1, "x", connect.CodeInvalidArgument},
		{"b1", "nul\x00.txt", 1, "x", connect.CodeInvalidArgument},
		{"../b1", "x.txt", 1, "x", connect.CodeInvalidArgument},
		{"", "x.txt", 1, "x", connect.CodeInvalidArgument},
		{"b2", "x.txt", 1, "x", connect.CodeNotFound},
	} {
		if _, err := upload(h, c.bot, c.name, c.size, c.data); connect.CodeOf(err) != c.want {
			t.Errorf("upload(%q, %q) = %v, want %v", c.bot, c.name, err, c.want)
		}
	}
	if left, _ := os.ReadDir(uploads); len(left) != 2 {
		t.Fatalf("files left behind: %v", left)
	}

	// uploads/ made a link out of the folder.
	os.RemoveAll(uploads)
	os.Symlink(home, uploads)
	_, err = upload(h, "b1", "x.txt", 1, "x")
	code(t, err, connect.CodePermissionDenied)
	if _, err := os.Stat(filepath.Join(home, "x.txt")); err == nil {
		t.Fatal("written through the link")
	}

	_, err = upload(New(Config{}), "b1", "x.txt", 1, "x")
	code(t, err, connect.CodeFailedPrecondition)
}

// A download is a file of the assistant's folder, by a path inside it,
// absolute or relative: no way out by .. or a link.
func TestDownload(t *testing.T) {
	noHerHere(t)
	home := herHomeWithBot(t)
	h := onAt(t, home)
	folder := filepath.Join(home, "bots", "b1", "files")
	os.MkdirAll(filepath.Join(folder, "out"), 0o700)
	os.WriteFile(filepath.Join(folder, "out", "report.md"), []byte("# Report\n"), 0o600)
	os.Symlink(filepath.Join(home, "config.toml"), filepath.Join(folder, "link.toml"))
	os.Symlink(filepath.Join(folder, "out", "report.md"), filepath.Join(folder, "inner.md"))

	for _, p := range []string{"out/report.md", filepath.Join(folder, "out", "report.md"), "inner.md", "out/../out/report.md"} {
		start, data, err := download(h, "b1", p)
		if err != nil || start.GetName() != "report.md" || start.GetSize() != 9 || data != "# Report\n" {
			t.Fatalf("download(%s) = %v %q %v", p, start, data, err)
		}
	}
	for p, want := range map[string]connect.Code{
		"../bot.json":                      connect.CodePermissionDenied,
		"../../../config.toml":             connect.CodePermissionDenied,
		filepath.Join(home, "config.toml"): connect.CodePermissionDenied,
		"link.toml":                        connect.CodePermissionDenied,
		"out":                              connect.CodeInvalidArgument,
		"missing.txt":                      connect.CodeNotFound,
		"":                                 connect.CodeInvalidArgument,
	} {
		if _, _, err := download(h, "b1", p); connect.CodeOf(err) != want {
			t.Errorf("download(%q) = %v, want %v", p, err, want)
		}
	}
}
