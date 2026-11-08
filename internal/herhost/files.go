package herhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"connectrpc.com/connect"

	herv1 "github.com/hermeshai/hermesh/gen/go/hermesh/her/v1"
)

// Files in and out of an assistant's folder, <her home>/bots/<id>/files
// (docs/her-server.md §6.3), straight from the disk: her need not run.

const chunk = 64 << 10

var botID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Upload writes the bytes from r into the assistant's uploads/ under the
// name start gives (a number added while it is taken), when they add up to
// start's size.
func (h *Host) Upload(ctx context.Context, start *herv1.UploadStart, r io.Reader) (*herv1.UploadResponse, error) {
	name := start.GetName()
	if !fileName(name) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not a file's name", name))
	}
	folder, err := h.folder(start.GetBotId())
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(folder, "uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if dir, err = inside(folder, dir); err != nil {
		return nil, err
	}
	f, path, err := create(dir, name)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, io.LimitReader(r, int64(start.GetSize())+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && uint64(n) != start.GetSize() {
		err = connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the upload is not %d bytes", start.GetSize()))
	}
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	return &herv1.UploadResponse{Path: path}, nil
}

// Download sends a file of the assistant's folder: `start`, then its bytes.
func (h *Host) Download(ctx context.Context, req *herv1.DownloadRequest, send func(*herv1.DownloadResponse) error) error {
	folder, err := h.folder(req.GetBotId())
	if err != nil {
		return err
	}
	p := req.GetPath()
	if p == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("no path"))
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(folder, p)
	}
	if p, err = inside(folder, p); err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is not a file", req.GetPath()))
	}
	if err := send(&herv1.DownloadResponse{Part: &herv1.DownloadResponse_Start{Start: &herv1.DownloadStart{Name: filepath.Base(p), Size: uint64(st.Size())}}}); err != nil {
		return err
	}
	r, buf := io.LimitReader(f, st.Size()), make([]byte, chunk)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if err := send(&herv1.DownloadResponse{Part: &herv1.DownloadResponse_Data{Data: bytes.Clone(buf[:n])}}); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// folder is the assistant's folder, its symlinks resolved, while Her is on.
func (h *Host) folder(id string) (string, error) {
	h.mu.Lock()
	on, home := h.stop != nil, h.home
	h.mu.Unlock()
	switch {
	case !on:
		return "", errOff
	case home == "":
		return "", connect.NewError(connect.CodeUnavailable, errors.New("her is starting"))
	case !botID.MatchString(id):
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q is not an assistant's id", id))
	}
	bot := filepath.Join(home, "bots", id)
	if _, err := os.Stat(filepath.Join(bot, "bot.json")); err != nil {
		return "", connect.NewError(connect.CodeNotFound, fmt.Errorf("no assistant %s", id))
	}
	dir := filepath.Join(bot, "files")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return inside(bot, dir)
}

// inside is p with its symlinks resolved, when that is dir or inside it.
func inside(dir, p string) (string, error) {
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	q, err := filepath.EvalSymlinks(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", connect.NewError(connect.CodeNotFound, err)
	}
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(d, q); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s is outside the assistant's folder", p))
	}
	return q, nil
}

// fileName: whether name is a file's name, without folders.
func fileName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

// create makes a new file name in dir, with a number added while the name
// is taken ("notes-2.txt"), as attachments are named.
func create(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s-%d%s", base, i, ext)
		}
		p := filepath.Join(dir, n)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, fs.ErrExist) {
			return f, p, err
		}
	}
}
