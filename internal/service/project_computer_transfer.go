package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const computerTransferLimit = 25 << 20

func safeTransferPath(path string) bool {
	if !filepath.IsLocal(path) || path == "." {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(path)), "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	return true
}

// Called under the task's desktop reservation. Paths stay inside the current worktree and Files.
func (s *ProjectComputerService) transfer(ctx context.Context, id, direction, local, remote string) (string, error) {
	if !safeTransferPath(local) || !safeTransferPath(remote) {
		return "", errors.New("choose relative file paths without hidden directories")
	}
	base, err := s.repo.TransferRoot(ctx, id)
	if err != nil || base == "" {
		return "", errors.New("task has no project directory")
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", errors.New("task directory unavailable")
	}
	defer root.Close()
	conn, err := s.Connection(ctx, id)
	if err != nil {
		return "", err
	}
	target := conn.Endpoint + "/files?path=" + url.QueryEscape("Files/"+filepath.ToSlash(remote))
	var data []byte
	method := http.MethodGet
	if direction == "to_computer" {
		file, err := openComputerTransferSource(root, local)
		if err != nil {
			return "", err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > computerTransferLimit {
			return "", errors.New("choose a regular file up to 25 MB")
		}
		data, err = io.ReadAll(io.LimitReader(file, computerTransferLimit+1))
		if err != nil {
			return "", err
		}
		if len(data) > computerTransferLimit {
			return "", errors.New("file exceeds 25 MB")
		}
		method = http.MethodPut
	} else if direction != "from_computer" {
		return "", errors.New("invalid transfer direction")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+conn.Token)
	res, err := s.client.Do(req)
	if err != nil {
		return "", errors.New("computer transfer failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", errors.New("computer transfer failed; check paths and choose a new destination filename")
	}
	if direction == "from_computer" {
		if !strings.HasPrefix(res.Header.Get("Content-Type"), "application/octet-stream") {
			return "", errors.New("choose a file, not a directory")
		}
		data, err = io.ReadAll(io.LimitReader(res.Body, computerTransferLimit+1))
		if err != nil {
			return "", err
		}
		if len(data) > computerTransferLimit {
			return "", errors.New("file exceeds 25 MB")
		}
		if err = root.MkdirAll(filepath.Dir(local), 0700); err != nil {
			return "", err
		}
		file, err := root.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return "", errors.New("destination already exists or is unavailable")
		}
		_, err = file.Write(data)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			root.Remove(local)
			return "", errors.New("could not save transferred file")
		}
	}
	return "File transferred", nil
}

// HumanFiles is the user's file picker path, separate from model tool execution.
func (s *ProjectComputerService) HumanFiles(ctx context.Context, id, path string, upload []byte, write bool) ([]byte, string, error) {
	if s.taskID == "" {
		return nil, "", errors.New("choose a task desktop")
	}
	if path != "" && !safeTransferPath(path) {
		return nil, "", errors.New("choose a relative path inside Files")
	}
	if write && (path == "" || len(upload) > computerTransferLimit) {
		return nil, "", errors.New("choose a file up to 25 MB")
	}
	gate := s.lifecycle(id)
	gate.RLock()
	defer gate.RUnlock()
	var data []byte
	var contentType string
	err := s.WithHumanInput(ctx, id, func() error {
		conn, err := s.Connection(ctx, id)
		if err != nil {
			return err
		}
		method := http.MethodGet
		if write {
			method = http.MethodPut
		}
		req, err := http.NewRequestWithContext(ctx, method, conn.Endpoint+"/files?path="+url.QueryEscape("Files/"+path), bytes.NewReader(upload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+conn.Token)
		res, err := s.client.Do(req)
		if err != nil {
			return errors.New("computer files unavailable")
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return errors.New("file operation failed; check the path and use a new filename for uploads")
		}
		data, err = io.ReadAll(io.LimitReader(res.Body, computerTransferLimit+1))
		if err != nil {
			return err
		}
		if len(data) > computerTransferLimit {
			return errors.New("file exceeds 25 MB")
		}
		contentType = res.Header.Get("Content-Type")
		return nil
	})
	return data, contentType, err
}

// Check before opening to avoid touching special files, then recheck the opened
// descriptor. Nonblocking open on Unix also protects against FIFO replacement
// between the initial stat and open.
func openComputerTransferSource(root *os.Root, path string) (*os.File, error) {
	info, err := root.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > computerTransferLimit {
		return nil, errors.New("choose a regular file up to 25 MB")
	}
	f, err := openComputerTransferFile(root, path)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() > computerTransferLimit || !os.SameFile(info, opened) {
		f.Close()
		return nil, errors.New("choose an unchanged regular file up to 25 MB")
	}
	return f, nil
}
