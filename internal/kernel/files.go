package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"
	"golang.org/x/text/encoding/simplifiedchinese"
	xunicode "golang.org/x/text/encoding/unicode"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	sessionv1 "github.com/hermeshai/hermesh/gen/go/hermesh/session/v1"
	"github.com/hermeshai/hermesh/internal/kernel/vcs"
)

// A session's files, read when opened (docs/session-files.md §6): as a turn left
// them, from its checkpoint's tree, or as they are in the session's folder now.
// Nothing outside the folder is read, and never `.git`'s insides. The listing's
// order and the text's decoding are FinxWork's (finx-core/src/files.rs).

const (
	// Text over this is only described.
	maxFileText = 5 << 20
	// Images over this are only described.
	maxFileImage = 10 << 20
	// Entries of one folder given at most.
	maxFileEntries = 5000
	// A ReadFileBytes call's most.
	maxFileChunk = 4 << 20
	// A turn's file over this is not compared with the folder's by content.
	maxFileCompare = 64 << 20
)

var (
	errFileOutside = connect.NewError(connect.CodePermissionDenied, errors.New("outside the session's folder"))
	errFileMissing = connect.NewError(connect.CodeNotFound, errors.New("no such file"))
)

// filesScope is where one request reads a session's files from.
type filesScope struct {
	sessionID string
	runID     string
	folder    string
	// The folder with its links resolved; "" when it is gone.
	real string
	gone bool
	// Where git runs: the folder, else the project's repository; "" with no repository.
	gitDir string
	// The folder's place in its repository's tree ("" or "sub/dir/").
	prefix string
	// The checkpoint's tree read from, and its turn; "" and 0: the folder now.
	tree  string
	turn  uint32
	owner bool
}

func (e *Engine) filesScope(ctx context.Context, p *Principal, sessionID, runID string) (*filesScope, error) {
	if p == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing principal"))
	}
	sess, err := e.st.Session(sessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if !p.canViewSession(sess) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	}
	ws, err := e.st.Workspace(sess.ProjectId)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the session's project is gone"))
	}
	// Never resolveWorkdir: it makes a released worktree again, and a look at a file must not.
	worktree := sess.GetWorkspaceStrategy().GetMode() == identityv1.Workspace_WORKSPACE_WORKTREE
	s := &filesScope{
		sessionID: sessionID,
		runID:     runID,
		folder:    filepath.Clean(ws.Path),
		owner:     p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_OWNER || p.Actor.GetKind() == sessionv1.ActorKind_ACTOR_KIND_HER,
	}
	if worktree {
		s.folder = sess.GetWorkspaceStrategy().GetWorktreePath()
		if s.folder == "" {
			s.folder = filepath.Join(e.vcs.WorktreesRoot(), sessionID)
		}
		s.folder = filepath.Clean(s.folder)
	}
	if st, err := os.Stat(s.folder); err == nil && st.IsDir() {
		s.real, _ = filepath.EvalSymlinks(s.folder)
	}
	s.gone = s.real == ""
	switch {
	case !s.gone && vcs.IsGitRepo(ctx, s.folder):
		s.gitDir = s.folder
		s.prefix = vcs.Prefix(ctx, s.folder)
	case vcs.IsGitRepo(ctx, ws.Path):
		s.gitDir = ws.Path
		if !worktree {
			s.prefix = vcs.Prefix(ctx, ws.Path)
		}
	}
	if s.gitDir == "" || (runID == "" && !s.gone) {
		return s, nil
	}
	cps, _ := e.st.Checkpoints(sessionID)
	for _, cp := range cps {
		// A turn's own; or, the folder gone, the last turn's.
		if cp.GetGitTreeOid() != "" && (cp.GetRunId() == runID || runID == "") {
			s.tree, s.turn = cp.GetGitTreeOid(), cp.GetRunOrdinal()
		}
	}
	return s, nil
}

// rel is a path as a reply wrote it, within the folder ("" for the folder itself);
// outside it, ok is false and abs is where it points.
func (s *filesScope) rel(asked string) (rel, abs string, ok bool) {
	asked = strings.TrimSpace(asked)
	if strings.HasPrefix(asked, "file://") {
		if u, err := url.Parse(asked); err == nil {
			asked = u.Path
		}
	}
	if asked == "~" || strings.HasPrefix(asked, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			asked = filepath.Join(home, strings.TrimPrefix(asked, "~"))
		}
	}
	abs = asked
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.folder, asked)
	}
	abs = filepath.Clean(abs)
	for _, base := range []string{s.folder, s.real} {
		if base == "" {
			continue
		}
		r, err := filepath.Rel(base, abs)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			continue
		}
		r = filepath.ToSlash(r)
		if r == "." {
			r = ""
		}
		if slices.Contains(strings.Split(r, "/"), ".git") {
			return "", abs, false
		}
		return r, abs, true
	}
	return "", abs, false
}

// now opens rel in the folder as it is: links resolved, still inside it.
func (s *filesScope) now(ctx context.Context, rel string) (string, os.FileInfo, error) {
	if s.gone {
		return "", nil, errFileMissing
	}
	real, err := filepath.EvalSymlinks(filepath.Join(s.real, filepath.FromSlash(rel)))
	if err != nil {
		return "", nil, errFileMissing
	}
	if r, err := filepath.Rel(s.real, real); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || slices.Contains(strings.Split(filepath.ToSlash(r), "/"), ".git") {
		return "", nil, errFileOutside
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", nil, errFileMissing
	}
	// What Git ignores (a .env) is the owner's to read, not a grant's.
	if !s.owner && !st.IsDir() && s.gitDir == s.folder && vcs.Ignored(ctx, s.folder, rel) {
		return "", nil, connect.NewError(connect.CodePermissionDenied, errors.New("an ignored file"))
	}
	return real, st, nil
}

// inTree looks rel up in the scope's tree; nil when there is no tree or it is not there.
func (s *filesScope) inTree(ctx context.Context, rel string) *vcs.TreeEntry {
	if s.tree == "" {
		return nil
	}
	got, err := vcs.TreeEntries(ctx, s.gitDir, s.tree, []string{s.prefix + rel})
	if err != nil || len(got) != 1 {
		return nil
	}
	return &got[0]
}

func (e *Engine) ResolveFiles(ctx context.Context, p *Principal, req *sessionv1.ResolveFilesRequest) (*sessionv1.ResolveFilesResponse, error) {
	s, err := e.filesScope(ctx, p, req.GetSessionId(), req.GetRunId())
	if err != nil {
		return nil, err
	}
	paths := req.GetPaths()
	if len(paths) > 200 {
		paths = paths[:200]
	}
	out := &sessionv1.ResolveFilesResponse{Folder: s.folder, FolderGone: s.gone}
	rels := make([]string, len(paths))
	inside := make([]bool, len(paths))
	var lookups []string
	for i, a := range paths {
		r, abs, ok := s.rel(a)
		rels[i], inside[i] = r, ok
		out.Files = append(out.Files, &sessionv1.ResolvedFile{Asked: a, Kind: sessionv1.FileRefKind_FILE_REF_KIND_OUTSIDE, Path: abs})
		if ok && r != "" {
			lookups = append(lookups, s.prefix+r)
		}
	}
	found := map[string]vcs.TreeEntry{}
	if s.tree != "" && len(lookups) > 0 {
		got, _ := vcs.TreeEntries(ctx, s.gitDir, s.tree, lookups)
		for _, x := range got {
			found[x.Path] = x
		}
	}
	for i, f := range out.Files {
		if !inside[i] {
			if !s.owner {
				continue
			}
			if s.runID != "" || s.gone {
				if tib, ok := e.st.TurnImageBlob(s.sessionID, s.runID, f.Path); ok {
					if _, err := os.Stat(e.st.BlobPath(tib.BlobSHA256)); err == nil {
						f.Kind, f.Size, f.Turn = sessionv1.FileRefKind_FILE_REF_KIND_FILE, uint64(tib.Size), tib.Turn
						continue
					}
				}
			}
			if _, size, ok := outsideReadable(f.Path); ok {
				f.Kind, f.Size = sessionv1.FileRefKind_FILE_REF_KIND_FILE, uint64(size)
			}
			continue
		}
		abs := f.Path
		f.Path = rels[i]
		if rels[i] == "" {
			f.Kind = sessionv1.FileRefKind_FILE_REF_KIND_FOLDER
			continue
		}
		if x, ok := found[s.prefix+rels[i]]; ok {
			f.Turn = s.turn
			f.Kind = sessionv1.FileRefKind_FILE_REF_KIND_FILE
			if x.Type == "tree" {
				f.Kind = sessionv1.FileRefKind_FILE_REF_KIND_FOLDER
			} else {
				f.Size = uint64(x.Size)
			}
			continue
		}
		if s.owner && (s.runID != "" || s.gone) {
			if tib, ok := e.st.TurnImageBlob(s.sessionID, s.runID, abs); ok {
				if _, err := os.Stat(e.st.BlobPath(tib.BlobSHA256)); err == nil {
					f.Kind, f.Size, f.Turn = sessionv1.FileRefKind_FILE_REF_KIND_FILE, uint64(tib.Size), tib.Turn
					continue
				}
			}
		}
		// A folder's own path is not an entry ls-tree names by itself; the folder now, or nothing.
		switch _, st, err := s.now(ctx, rels[i]); {
		case err == nil && st.IsDir():
			f.Kind = sessionv1.FileRefKind_FILE_REF_KIND_FOLDER
		case err == nil:
			f.Kind, f.Size = sessionv1.FileRefKind_FILE_REF_KIND_FILE, uint64(st.Size())
		case errors.Is(err, errFileOutside):
			f.Kind = sessionv1.FileRefKind_FILE_REF_KIND_OUTSIDE
		default:
			f.Kind = sessionv1.FileRefKind_FILE_REF_KIND_MISSING
			if s.tree != "" && treeHasDir(ctx, s, rels[i]) {
				f.Kind, f.Turn = sessionv1.FileRefKind_FILE_REF_KIND_FOLDER, s.turn
			}
		}
	}
	return out, nil
}

func treeHasDir(ctx context.Context, s *filesScope, rel string) bool {
	got, err := vcs.TreeEntries(ctx, s.gitDir, s.tree, []string{s.prefix + rel + "/"})
	return err == nil && len(got) > 0
}

func (e *Engine) ListFiles(ctx context.Context, p *Principal, req *sessionv1.ListFilesRequest) (*sessionv1.ListFilesResponse, error) {
	s, err := e.filesScope(ctx, p, req.GetSessionId(), req.GetRunId())
	if err != nil {
		return nil, err
	}
	dir, _, ok := s.rel(req.GetDir())
	if !ok {
		return nil, errFileOutside
	}
	out := &sessionv1.ListFilesResponse{Dir: dir, Folder: s.folder, FolderGone: s.gone}
	if s.tree != "" {
		want := s.prefix
		if dir != "" {
			want = s.prefix + dir + "/"
		}
		got, err := vcs.TreeEntries(ctx, s.gitDir, s.tree, []string{want})
		if err != nil {
			return nil, err
		}
		if len(got) == 0 && dir != "" {
			return nil, errFileMissing
		}
		out.Turn = s.turn
		for _, x := range got {
			out.Entries = append(out.Entries, &sessionv1.FileEntry{Name: path.Base(x.Path), Dir: x.Type == "tree", Link: x.Link(), Size: uint64(x.Size)})
		}
	} else {
		real, st, err := s.now(ctx, dir)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("not a folder"))
		}
		items, err := os.ReadDir(real)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			link := it.Type()&os.ModeSymlink != 0
			// A link is what it points to; a broken one is a file of no size.
			var isDir bool
			var size uint64
			if st, err := os.Stat(filepath.Join(real, it.Name())); err == nil {
				isDir = st.IsDir()
				if st.Mode().IsRegular() {
					size = uint64(st.Size())
				}
			}
			out.Entries = append(out.Entries, &sessionv1.FileEntry{Name: it.Name(), Dir: isDir, Link: link, Size: size})
		}
	}
	out.Entries = slices.DeleteFunc(out.Entries, func(x *sessionv1.FileEntry) bool { return x.Name == ".git" || x.Name == ".DS_Store" })
	slices.SortStableFunc(out.Entries, func(a, b *sessionv1.FileEntry) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		return naturalCompare(a.Name, b.Name)
	})
	if len(out.Entries) > maxFileEntries {
		out.Entries, out.Truncated = out.Entries[:maxFileEntries], true
	}
	return out, nil
}

// fileSource is a file found for reading: a blob of the turn's tree, a pinned turn image blob,
// a file in the folder now, or (for the owner) a verified raster image outside the folder.
type fileSource struct {
	path      string
	inside    bool
	size      int64
	blob      string
	storeBlob string
	turn      uint32
	real      string
}

func isRasterImageExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

// outsideImage checks whether abs points to a real raster image <= maxFileImage by
// resolving symlinks, checking extension & regular-file bounds, and verifying magic bytes.
func outsideImage(abs string) (string, int64, bool) {
	if !isRasterImageExt(abs) {
		return "", 0, false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", 0, false
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > maxFileImage {
		return "", 0, false
	}
	fh, err := os.Open(real)
	if err != nil {
		return "", 0, false
	}
	var head [16]byte
	n, _ := io.ReadFull(fh, head[:])
	_ = fh.Close()
	if n == 0 || imageMime(head[:n]) == "" {
		return "", 0, false
	}
	return real, st.Size(), true
}

func isMarkdownExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

func isOutsideArtifactPath(real string) bool {
	if st, err := os.Stat(real + ".metadata.json"); err == nil && st.Mode().IsRegular() {
		return true
	}
	slash := filepath.ToSlash(real)
	return strings.Contains(slash, "/.gemini/") && strings.Contains(slash, "/brain/")
}

// outsideArtifactMarkdown allows the owner to open Markdown artifacts written outside the
// worktree (such as Antigravity ~/.gemini/.../brain/*.md files or files with a .metadata.json
// sidecar), after resolving symlinks so links cannot escape to arbitrary files.
func outsideArtifactMarkdown(abs string) (string, int64, bool) {
	if !isMarkdownExt(abs) {
		return "", 0, false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil || !isMarkdownExt(real) || !isOutsideArtifactPath(real) {
		return "", 0, false
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > maxFileText {
		return "", 0, false
	}
	return real, st.Size(), true
}

func outsideReadable(abs string) (string, int64, bool) {
	if real, size, ok := outsideImage(abs); ok {
		return real, size, true
	}
	return outsideArtifactMarkdown(abs)
}

func validOutsideBytes(real string, data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if len(data) <= maxFileImage && imageMime(data) != "" {
		return true
	}
	return isMarkdownExt(real) && isOutsideArtifactPath(real) && len(data) <= maxFileText && utf8.Valid(data)
}

func (e *Engine) fileSource(ctx context.Context, s *filesScope, asked string) (*fileSource, error) {
	rel, abs, ok := s.rel(asked)
	if !ok {
		if !s.owner {
			return nil, errFileOutside
		}
		if s.runID != "" || s.gone {
			if tib, ok := e.st.TurnImageBlob(s.sessionID, s.runID, abs); ok {
				if _, err := os.Stat(e.st.BlobPath(tib.BlobSHA256)); err == nil {
					return &fileSource{path: abs, size: tib.Size, storeBlob: tib.BlobSHA256, turn: tib.Turn}, nil
				}
			}
		}
		if real, size, ok := outsideReadable(abs); ok {
			return &fileSource{path: abs, size: size, real: real}, nil
		}
		return nil, errFileOutside
	}
	if rel == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a folder"))
	}
	if x := s.inTree(ctx, rel); x != nil {
		if x.Type != "blob" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a folder"))
		}
		return &fileSource{path: rel, inside: true, size: x.Size, blob: x.OID, turn: s.turn}, nil
	}
	if s.owner && (s.runID != "" || s.gone) {
		if tib, ok := e.st.TurnImageBlob(s.sessionID, s.runID, abs); ok {
			if _, err := os.Stat(e.st.BlobPath(tib.BlobSHA256)); err == nil {
				return &fileSource{path: rel, inside: true, size: tib.Size, storeBlob: tib.BlobSHA256, turn: tib.Turn}, nil
			}
		}
	}
	real, st, err := s.now(ctx, rel)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("not a file"))
	}
	return &fileSource{path: rel, inside: true, size: st.Size(), real: real}, nil
}

func (f *fileSource) bytes(ctx context.Context, e *Engine, s *filesScope) ([]byte, error) {
	if f.blob != "" {
		return blobBytes(ctx, s.gitDir, f.blob)
	}
	if f.storeBlob != "" {
		data, _, _, err := e.st.GetBlob(f.storeBlob)
		if err != nil {
			return nil, errFileMissing
		}
		return data, nil
	}
	data, err := os.ReadFile(f.real)
	if err != nil {
		if !f.inside {
			return nil, errFileOutside
		}
		return nil, err
	}
	if !f.inside && !validOutsideBytes(f.real, data) {
		return nil, errFileOutside
	}
	return data, nil
}

func (e *Engine) ReadFile(ctx context.Context, p *Principal, req *sessionv1.ReadFileRequest) (*sessionv1.ReadFileResponse, error) {
	s, err := e.filesScope(ctx, p, req.GetSessionId(), req.GetRunId())
	if err != nil {
		return nil, err
	}
	f, err := e.fileSource(ctx, s, req.GetPath())
	if err != nil {
		return nil, err
	}
	out := &sessionv1.ReadFileResponse{Path: f.path, Size: uint64(f.size), Kind: sessionv1.FileContentKind_FILE_CONTENT_KIND_TOO_LARGE, MimeType: mimeOf(f.path, nil), Folder: s.folder}
	if f.turn > 0 {
		out.Turn = f.turn
	}
	if f.size <= maxFileImage {
		data, err := f.bytes(ctx, e, s)
		if err != nil {
			return nil, err
		}
		describe(out, data)
		if f.turn > 0 {
			if f.inside {
				out.Now = compareNow(ctx, s, f.path, data)
			} else {
				out.Now = compareOutsideNow(f.path, data)
			}
		}
	} else if f.turn > 0 && f.inside {
		out.Now = compareNow(ctx, s, f.path, nil)
	}
	return out, nil
}

// describe fills what a file's bytes are: an image by its signature, text by its
// decoding, else binary; too large past the limits.
func describe(out *sessionv1.ReadFileResponse, data []byte) {
	sum := sha256.Sum256(data)
	if m := imageMime(data); m != "" {
		out.Kind, out.Image, out.MimeType, out.Hash = sessionv1.FileContentKind_FILE_CONTENT_KIND_IMAGE, data, m, hex.EncodeToString(sum[:])
		return
	}
	if len(data) > maxFileText {
		return
	}
	out.Hash = hex.EncodeToString(sum[:])
	if text, enc, ok := decodeText(data); ok {
		out.Kind, out.Text, out.Encoding = sessionv1.FileContentKind_FILE_CONTENT_KIND_TEXT, text, enc
		return
	}
	out.Kind = sessionv1.FileContentKind_FILE_CONTENT_KIND_BINARY
}

// compareNow tells whether the folder's file is still what the turn left (`data`,
// nil when too large to have been read).
func compareNow(ctx context.Context, s *filesScope, rel string, data []byte) sessionv1.FileNow {
	if s.gone {
		return sessionv1.FileNow_FILE_NOW_UNKNOWN
	}
	real, st, err := s.now(ctx, rel)
	switch {
	case err != nil || !st.Mode().IsRegular():
		return sessionv1.FileNow_FILE_NOW_GONE
	case data == nil || st.Size() != int64(len(data)):
		if data != nil {
			return sessionv1.FileNow_FILE_NOW_CHANGED
		}
		return sessionv1.FileNow_FILE_NOW_UNKNOWN
	case st.Size() > maxFileCompare:
		return sessionv1.FileNow_FILE_NOW_UNKNOWN
	}
	cur, err := os.ReadFile(real)
	if err != nil || !bytes.Equal(cur, data) {
		return sessionv1.FileNow_FILE_NOW_CHANGED
	}
	return sessionv1.FileNow_FILE_NOW_SAME
}

func compareOutsideNow(abs string, data []byte) sessionv1.FileNow {
	real, size, ok := outsideReadable(abs)
	if !ok {
		return sessionv1.FileNow_FILE_NOW_GONE
	}
	if data == nil || size != int64(len(data)) {
		return sessionv1.FileNow_FILE_NOW_CHANGED
	}
	cur, err := os.ReadFile(real)
	if err != nil || !bytes.Equal(cur, data) {
		return sessionv1.FileNow_FILE_NOW_CHANGED
	}
	return sessionv1.FileNow_FILE_NOW_SAME
}

func (e *Engine) ReadFileBytes(ctx context.Context, p *Principal, req *sessionv1.ReadFileBytesRequest) (*sessionv1.ReadFileBytesResponse, error) {
	s, err := e.filesScope(ctx, p, req.GetSessionId(), req.GetRunId())
	if err != nil {
		return nil, err
	}
	f, err := e.fileSource(ctx, s, req.GetPath())
	if err != nil {
		return nil, err
	}
	n := int64(req.GetLength())
	if n <= 0 || n > maxFileChunk {
		n = maxFileChunk
	}
	off := int64(req.GetOffset())
	out := &sessionv1.ReadFileBytesResponse{Size: uint64(f.size)}
	if f.turn > 0 {
		out.Turn = f.turn
	}
	if off < f.size {
		n = min(n, f.size-off)
		if f.blob != "" || f.storeBlob != "" || !f.inside {
			data, err := f.bytes(ctx, e, s)
			if err != nil {
				return nil, err
			}
			if off < int64(len(data)) {
				out.Data = data[off:min(off+n, int64(len(data)))]
			}
		} else {
			fh, err := os.Open(f.real)
			if err != nil {
				return nil, errFileMissing
			}
			defer fh.Close()
			buf := make([]byte, n)
			got, err := fh.ReadAt(buf, off)
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			out.Data = buf[:got]
		}
	}
	head := out.Data
	if off != 0 {
		head = nil
	}
	out.MimeType = mimeOf(f.path, head)
	return out, nil
}

const maxPinnedTurnImages = 16

// pinTurnImages snapshots local raster images referenced by a finished turn's replies
// when they are not already captured in the turn's Git checkpoint tree (e.g. /tmp or
// ~/.gemini/.../brain screenshots, or gitignored build screenshots).
func (e *Engine) pinTurnImages(ctx context.Context, sessionID, runID string, ordinal uint32, workdir, treeOID string) {
	if sessionID == "" || runID == "" || workdir == "" {
		return
	}
	items, err := e.st.TurnItems(sessionID)
	if err != nil {
		return
	}
	var raw []string
	for _, it := range items {
		kind := it.GetKind()
		if it.GetRunId() == runID && (kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_ASSISTANT_MESSAGE || kind == sessionv1.TurnItemKind_TURN_ITEM_KIND_PROPOSED_PLAN) && it.GetText() != "" {
			raw = append(raw, replyImageCandidates(it.GetText())...)
		}
	}
	if len(raw) == 0 {
		return
	}
	s := &filesScope{sessionID: sessionID, runID: runID, folder: filepath.Clean(workdir), tree: treeOID, turn: max(ordinal, 1), owner: true}
	if st, err := os.Stat(s.folder); err == nil && st.IsDir() {
		s.real, _ = filepath.EvalSymlinks(s.folder)
	}
	s.gone = s.real == ""
	if treeOID != "" && !s.gone && vcs.IsGitRepo(ctx, s.folder) {
		s.gitDir = s.folder
		s.prefix = vcs.Prefix(ctx, s.folder)
	}
	seen := map[string]bool{}
	pinned := 0
	for _, c := range raw {
		if pinned >= maxPinnedTurnImages {
			break
		}
		rel, abs, inside := s.rel(c)
		if abs == "" || seen[abs] {
			continue
		}
		seen[abs] = true
		var real string
		if inside {
			if rel == "" || !isRasterImageExt(rel) || s.inTree(ctx, rel) != nil {
				continue
			}
			r, st, err := s.now(ctx, rel)
			if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > maxFileImage {
				continue
			}
			real = r
		} else {
			r, _, ok := outsideImage(abs)
			if !ok {
				continue
			}
			real = r
		}
		data, err := os.ReadFile(real)
		if err != nil || len(data) == 0 || len(data) > maxFileImage {
			continue
		}
		mimeType := imageMime(data)
		if mimeType == "" {
			continue
		}
		ref, err := e.st.PutBlob(filepath.Base(abs), mimeType, data)
		if err == nil {
			_ = e.st.PutTurnImageBlob(sessionID, runID, abs, ref.GetBlobSha256(), int64(len(data)), max(ordinal, 1))
			pinned++
		}
	}
}

// replyImageCandidates extracts candidate raster-image paths from Markdown links,
// images, and single-line inline code spans in an assistant reply.
func replyImageCandidates(text string) []string {
	var out []string
	add := func(raw string) {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			return
		}
		lower := strings.ToLower(t)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "mailto:") {
			return
		}
		if strings.HasPrefix(lower, "file:") {
			if u, err := url.Parse(t); err == nil {
				if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
					return
				}
				t = u.Path
			}
		} else if u, err := url.PathUnescape(t); err == nil {
			t = u
		}
		if i := strings.IndexByte(t, '#'); i >= 0 {
			t = t[:i]
		}
		if i := strings.LastIndexByte(t, ':'); i > 1 && !strings.ContainsAny(t[i+1:], `/\`) {
			// Strip trailing :line or :line-end if numeric.
			rest := strings.TrimRight(t[i+1:], "0123456789:-")
			if rest == "" && len(t[i+1:]) > 0 {
				t = t[:i]
			}
		}
		if isRasterImageExt(t) {
			out = append(out, t)
		}
	}
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case ']':
			if i+1 >= len(text) || text[i+1] != '(' {
				continue
			}
			j := i + 2
			for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
				j++
			}
			if j < len(text) && text[j] == '<' {
				end := strings.IndexByte(text[j+1:], '>')
				if end >= 0 {
					add(text[j+1 : j+1+end])
					if close := strings.IndexByte(text[j+1+end:], ')'); close >= 0 {
						i = j + 1 + end + close
					}
				}
				continue
			}
			depth := 1
			k := j
			for k < len(text) && text[k] != '\n' && depth > 0 {
				switch text[k] {
				case '(':
					depth++
				case ')':
					depth--
				}
				if depth > 0 {
					k++
				}
			}
			if depth == 0 {
				target := strings.TrimSpace(text[j:k])
				// Strip optional Markdown link title: path "title"
				if sp := strings.IndexAny(target, " \t"); sp > 0 && (strings.HasPrefix(target[sp+1:], `"`) || strings.HasPrefix(target[sp+1:], `'`)) {
					target = strings.TrimSpace(target[:sp])
				}
				add(target)
				i = k
			}
		case '`':
			if i+1 < len(text) && text[i+1] == '`' {
				for i < len(text) && text[i] == '`' {
					i++
				}
				continue
			}
			end := strings.IndexByte(text[i+1:], '`')
			if end > 0 && end <= 300 && !strings.ContainsRune(text[i+1:i+1+end], '\n') {
				add(text[i+1 : i+1+end])
				i += end + 1
			}
		}
	}
	return out
}

// mimeOf is a file's type by its name, else by its first bytes.
func mimeOf(name string, head []byte) string {
	if m := mime.TypeByExtension(strings.ToLower(path.Ext(name))); m != "" {
		return m
	}
	if m := imageMime(head); m != "" {
		return m
	}
	if len(head) > 0 {
		return http.DetectContentType(head)
	}
	return "application/octet-stream"
}

// imageMime is a picture's type by its signature: PNG, JPEG, GIF, WebP.
func imageMime(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return "image/gif"
	case len(b) >= 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return "image/webp"
	}
	return ""
}

// decodeText is the text of a file's bytes and how it was decoded: UTF-8 (a mark
// dropped), UTF-16 by its mark, else GB18030 when the bytes are that.
func decodeText(b []byte) (string, string, bool) {
	if body, ok := bytes.CutPrefix(b, []byte{0xEF, 0xBB, 0xBF}); ok {
		return string(body), "utf-8-bom", utf8.Valid(body)
	}
	for _, x := range []struct {
		mark []byte
		end  xunicode.Endianness
		name string
	}{{[]byte{0xFF, 0xFE}, xunicode.LittleEndian, "utf-16le"}, {[]byte{0xFE, 0xFF}, xunicode.BigEndian, "utf-16be"}} {
		if body, ok := bytes.CutPrefix(b, x.mark); ok {
			text, err := xunicode.UTF16(x.end, xunicode.IgnoreBOM).NewDecoder().Bytes(body)
			return string(text), x.name, err == nil && len(body)%2 == 0
		}
	}
	if bytes.IndexByte(b[:min(len(b), 8192)], 0) >= 0 {
		return "", "", false
	}
	if utf8.Valid(b) {
		return string(b), "utf-8", true
	}
	text, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b)
	if err != nil || bytes.ContainsRune(text, utf8.RuneError) {
		return "", "", false
	}
	return string(text), "gb18030", true
}

// naturalCompare compares names as people read them: case aside, runs of digits by value.
func naturalCompare(a, b string) int {
	x, y := []rune(a), []rune(b)
	i, j := 0, 0
	for i < len(x) && j < len(y) {
		if isDigit(x[i]) && isDigit(y[j]) {
			si, sj := i, j
			for i < len(x) && isDigit(x[i]) {
				i++
			}
			for j < len(y) && isDigit(y[j]) {
				j++
			}
			m := strings.TrimLeft(string(x[si:i]), "0")
			n := strings.TrimLeft(string(y[sj:j]), "0")
			if len(m) != len(n) {
				return cmpInt(len(m), len(n))
			}
			if c := strings.Compare(m, n); c != 0 {
				return c
			}
			continue
		}
		if c, d := unicode.ToLower(x[i]), unicode.ToLower(y[j]); c != d {
			return cmpInt(int(c), int(d))
		}
		i++
		j++
	}
	switch {
	case i < len(x):
		return 1
	case j < len(y):
		return -1
	}
	return strings.Compare(a, b)
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// A turn's blobs read for ranges, kept a while (a page's video read a part at a time).
var turnBlobs = struct {
	sync.Mutex
	order []string
	data  map[string][]byte
	size  int
}{data: map[string][]byte{}}

const turnBlobsMax = 64 << 20

func blobBytes(ctx context.Context, dir, oid string) ([]byte, error) {
	turnBlobs.Lock()
	if b, ok := turnBlobs.data[oid]; ok {
		turnBlobs.Unlock()
		return b, nil
	}
	turnBlobs.Unlock()
	b, err := vcs.ReadBlob(ctx, dir, oid)
	if err != nil {
		return nil, err
	}
	if len(b) <= turnBlobsMax/4 {
		turnBlobs.Lock()
		if _, ok := turnBlobs.data[oid]; !ok {
			turnBlobs.data[oid] = b
			turnBlobs.order = append(turnBlobs.order, oid)
			turnBlobs.size += len(b)
			for turnBlobs.size > turnBlobsMax && len(turnBlobs.order) > 0 {
				old := turnBlobs.order[0]
				turnBlobs.order = turnBlobs.order[1:]
				turnBlobs.size -= len(turnBlobs.data[old])
				delete(turnBlobs.data, old)
			}
		}
		turnBlobs.Unlock()
	}
	return b, nil
}
