package handler

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/pkg/filecontent"
)

const stagedUploadTTL = 24 * time.Hour

// uploadFile is one uploaded file: a request part on this server, or bytes
// staged on the server owning the target task or project.
type uploadFile struct {
	Name        string
	Size        int64
	ContentType string
	Token       string
	header      *multipart.FileHeader
}

// open returns the file's bytes and size. stageKey is the verified task or
// project the staged bytes were written under.
func (f uploadFile) open(stageKey string) (io.ReadCloser, int64, error) {
	if f.header != nil {
		src, err := f.header.Open()
		return src, f.header.Size, err
	}
	path, err := stagedUploadPath(stageKey, f.Token, f.Name)
	if err != nil {
		return nil, 0, err
	}
	src, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	info, err := src.Stat()
	if err != nil {
		src.Close()
		return nil, 0, err
	}
	return src, info.Size(), nil
}

func requestUploads(headers []*multipart.FileHeader) []uploadFile {
	files := make([]uploadFile, len(headers))
	for i, header := range headers {
		files[i] = uploadFile{Name: header.Filename, Size: header.Size, ContentType: header.Header.Get("Content-Type"), header: header}
	}
	return files
}

func stagedUploadDir(stageKey, token string) (string, error) {
	if stageKey == "" || stageKey != filepath.Base(stageKey) || stageKey == "." || stageKey == ".." || !isValidPendingAttachmentSessionID(token) {
		return "", statusErr(http.StatusBadRequest, "invalid upload")
	}
	return filepath.Join(uploadsDir, "staged", stageKey, token), nil
}

func stagedUploadPath(stageKey, token, name string) (string, error) {
	dir, err := stagedUploadDir(stageKey, token)
	if err != nil {
		return "", err
	}
	base := filepath.Base(name)
	if base != name || base == "." || base == ".." || base == string(filepath.Separator) {
		return "", statusErr(http.StatusBadRequest, "invalid upload name")
	}
	return filepath.Join(dir, base), nil
}

// removeStagedUploads drops a request's staged bytes once its action ends.
func removeStagedUploads(stageKey string, files []uploadFile) {
	for _, f := range files {
		if f.header != nil {
			continue
		}
		if dir, err := stagedUploadDir(stageKey, f.Token); err == nil {
			_ = os.RemoveAll(dir)
		}
	}
}

// sweepStagedUploads removes uploads abandoned by a hub that went away mid-request.
func sweepStagedUploads() {
	root := filepath.Join(uploadsDir, "staged")
	keys, _ := os.ReadDir(root)
	for _, key := range keys {
		tokens, _ := os.ReadDir(filepath.Join(root, key.Name()))
		for _, token := range tokens {
			if info, err := token.Info(); err == nil && time.Since(info.ModTime()) > stagedUploadTTL {
				_ = os.RemoveAll(filepath.Join(root, key.Name(), token.Name()))
			}
		}
	}
}

type stageUploadArgs struct {
	TaskID    string
	ProjectID string
	Token     string
	Name      string
	Offset    int64
	Data      []byte `remote:"raw"`
}

func (a stageUploadArgs) key() string {
	if a.TaskID != "" {
		return a.TaskID
	}
	return a.ProjectID
}

var actStageUpload = defineAction("upload.stage", func(h *Handler, ctx context.Context, a stageUploadArgs) (string, authz.Permission, error) {
	if a.TaskID != "" {
		return editTask(h, ctx, a.TaskID)
	}
	return editProject(h, ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a stageUploadArgs) (struct{}, error) {
	path, err := stagedUploadPath(a.key(), a.Token, a.Name)
	if err != nil {
		return struct{}{}, err
	}
	if a.Offset+int64(len(a.Data)) > filecontent.UploadLimit(a.Name) {
		return struct{}{}, statusErr(http.StatusRequestEntityTooLarge, "request body too large")
	}
	if a.Offset == 0 {
		sweepStagedUploads()
		entries, _ := os.ReadDir(filepath.Dir(path))
		if len(entries) >= maxTaskAttachmentFilesPerRequest {
			return struct{}{}, statusErr(http.StatusBadRequest, "too many files")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return struct{}{}, err
		}
	}
	flags := os.O_WRONLY | os.O_APPEND
	if a.Offset == 0 {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return struct{}{}, statusErr(http.StatusConflict, "upload out of order")
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.Size() != a.Offset {
		return struct{}{}, statusErr(http.StatusConflict, "upload out of order")
	}
	_, err = f.Write(a.Data)
	return struct{}{}, err
})

// stageUploads sends files bound for a remote task (or, before one exists, a
// remote project) to its server in chunks. Local files are returned as is.
func (h *Handler) stageUploads(ctx context.Context, taskID, projectID string, files []uploadFile) ([]uploadFile, error) {
	owner := taskID
	if owner == "" {
		owner = projectID
	}
	if _, remote := remoteid.ServerOf(owner); !remote || len(files) == 0 {
		return files, nil
	}
	token := generateSessionID()
	buf := make([]byte, remoteFileChunk)
	staged := make([]uploadFile, len(files))
	usedNames := make(map[string]bool, len(files))
	for i, f := range files {
		f.Name = uniqueAttachmentName("", filepath.Base(f.Name), usedNames)
		if err := h.stageUpload(ctx, taskID, projectID, token, f, buf); err != nil {
			applog.Infof("[handler] staging upload %s failed: %v", f.Name, err)
			return nil, err
		}
		staged[i] = uploadFile{Name: filepath.Base(f.Name), Size: f.Size, ContentType: f.ContentType, Token: token}
	}
	return staged, nil
}

func (h *Handler) stageUpload(ctx context.Context, taskID, projectID, token string, f uploadFile, buf []byte) error {
	src, _, err := f.open("")
	if err != nil {
		return err
	}
	defer src.Close()
	var offset int64
	for {
		n, readErr := io.ReadFull(src, buf)
		if n > 0 || offset == 0 {
			args := stageUploadArgs{TaskID: taskID, ProjectID: projectID, Token: token, Name: filepath.Base(f.Name), Offset: offset, Data: buf[:n]}
			if _, err := actStageUpload(h, ctx, args); err != nil {
				return err
			}
			offset += int64(n)
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
