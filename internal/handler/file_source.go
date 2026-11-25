package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/service/authz"
)

// fileSource opens a file's original bytes, on this server or the one owning it.
type fileSource func(context.Context) (*fileHandle, error)

type fileHandle struct {
	io.ReadSeekCloser
	size    int64
	modTime time.Time
}

func localFile(path string) fileSource {
	return func(context.Context) (*fileHandle, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return nil, errors.New("not a regular file")
		}
		return &fileHandle{ReadSeekCloser: f, size: info.Size(), modTime: info.ModTime()}, nil
	}
}

func readAllLimited(f *fileHandle, limit int64) ([]byte, error) {
	if f.size > limit {
		return nil, fmt.Errorf("file exceeds the %d byte limit", limit)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds the %d byte limit", limit)
	}
	return data, err
}

const (
	fileKindAttachment     = "attachment"
	fileKindChatAttachment = "chat_attachment"
	fileKindGenerated      = "generated"
	fileKindPending        = "pending"
	remoteFileChunk        = 1 << 20
)

// fileReadArgs names a stored file by its row; the owning server resolves the
// path itself, so callers can never name arbitrary paths.
type fileReadArgs struct {
	Kind      string
	ID        string
	ProjectID string
	Offset    int64
}

type fileChunk struct {
	Size    int64
	ModTime time.Time
	Data    []byte `remote:"raw"`
}

// sourceFor reads a remote row's file over its server's socket and a local
// row's file from disk.
func (h *Handler) sourceFor(kind, id, projectID, path string) fileSource {
	return func(ctx context.Context) (*fileHandle, error) {
		r := &remoteFileReader{h: h, ctx: ctx, args: fileReadArgs{Kind: kind, ID: id, ProjectID: projectID}}
		remote, err := h.remoteAct(ctx, id, "file.read", r.args, &r.chunk)
		if !remote {
			return localFile(path)(ctx)
		}
		if err != nil {
			return nil, err
		}
		return &fileHandle{ReadSeekCloser: r, size: r.chunk.Size, modTime: r.chunk.ModTime}, nil
	}
}

type remoteFileReader struct {
	h     *Handler
	ctx   context.Context
	args  fileReadArgs
	chunk fileChunk
	pos   int64
}

func (r *remoteFileReader) Read(p []byte) (int, error) {
	if r.pos >= r.chunk.Size {
		return 0, io.EOF
	}
	if r.pos < r.args.Offset || r.pos >= r.args.Offset+int64(len(r.chunk.Data)) {
		args := r.args
		args.Offset = r.pos
		var next fileChunk
		if _, err := r.h.remoteAct(r.ctx, args.ID, "file.read", args, &next); err != nil {
			return 0, err
		}
		if len(next.Data) == 0 || next.Size != r.chunk.Size {
			return 0, io.ErrUnexpectedEOF
		}
		r.args, r.chunk = args, next
	}
	n := copy(p, r.chunk.Data[r.pos-r.args.Offset:])
	r.pos += int64(n)
	return n, nil
}

func (r *remoteFileReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		offset += r.pos
	case io.SeekEnd:
		offset += r.chunk.Size
	}
	if offset < 0 {
		return 0, errors.New("negative seek")
	}
	r.pos = offset
	return offset, nil
}

func (r *remoteFileReader) Close() error { return nil }

// storedFilePath resolves a file row on this server, scoped to its project.
func (h *Handler) storedFilePath(ctx context.Context, a fileReadArgs) (string, error) {
	notFound := statusErr(http.StatusNotFound, "file not found")
	switch a.Kind {
	case fileKindPending:
		session, name, ok := strings.Cut(a.ID, "/")
		if !ok || !isValidPendingAttachmentSessionID(session) || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
			return "", notFound
		}
		return filepath.Join(uploadsDir, "chat", "pending", session, name), nil
	case fileKindAttachment:
		if row, err := h.attachmentRepo.GetByIDForProject(ctx, a.ID, a.ProjectID); err == nil && row != nil {
			return row.FilePath, nil
		}
	case fileKindChatAttachment:
		if row, err := h.chatAttachmentRepo.GetByIDForProject(ctx, a.ID, a.ProjectID); err == nil && row != nil {
			return row.FilePath, nil
		}
	case fileKindGenerated:
		if row, err := h.attachmentRepo.GeneratedFile(ctx, a.ID, a.ProjectID); err == nil && row != nil {
			return row.FilePath, nil
		}
	}
	return "", notFound
}

func init() {
	registerAction("file.read", func(h *Handler, ctx context.Context, a fileReadArgs) (string, authz.Permission, error) {
		if _, err := h.storedFilePath(ctx, a); err != nil {
			return "", "", err
		}
		if a.Kind == fileKindPending {
			session, _, _ := strings.Cut(a.ID, "/")
			project, err := os.ReadFile(filepath.Join(uploadsDir, "chat", "pending-owners", session))
			if err != nil {
				return "", "", statusErr(http.StatusNotFound, "file not found")
			}
			return string(project), authz.View, nil
		}
		return a.ProjectID, authz.View, nil
	}, func(h *Handler, ctx context.Context, a fileReadArgs) (fileChunk, error) {
		path, err := h.storedFilePath(ctx, a)
		if err != nil {
			return fileChunk{}, err
		}
		f, err := localFile(path)(ctx)
		if err != nil {
			return fileChunk{}, statusErr(http.StatusNotFound, "file not found")
		}
		defer f.Close()
		if a.Offset < 0 || a.Offset > f.size {
			return fileChunk{}, statusErr(http.StatusRequestedRangeNotSatisfiable, "invalid offset")
		}
		if _, err := f.Seek(a.Offset, io.SeekStart); err != nil {
			return fileChunk{}, err
		}
		n := f.size - a.Offset
		if n > remoteFileChunk {
			n = remoteFileChunk
		}
		data := make([]byte, n)
		if _, err := io.ReadFull(f, data); err != nil {
			return fileChunk{}, err
		}
		return fileChunk{Size: f.size, ModTime: f.modTime, Data: data}, nil
	})
}
