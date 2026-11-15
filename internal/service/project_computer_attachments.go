package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func (s *ProjectComputerService) SetAttachmentDirectory(path string) {
	if s != nil {
		s.attachmentDirectory = path
	}
}
func (s *ProjectComputerService) attachmentTransfer(ctx context.Context, id string, input json.RawMessage) (string, error) {
	var args struct {
		Action       string `json:"action"`
		Kind         string `json:"attachment_kind"`
		AttachmentID string `json:"attachment_id"`
		Path         string `json:"computer_path"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", err
	}
	repo := s.repo.Attachments()
	if args.Action == "list" {
		items, err := repo.ListByTask(ctx, s.taskID)
		if err != nil {
			return "", err
		}
		out := []map[string]any{}
		for _, a := range items {
			out = append(out, map[string]any{"attachment_id": a.ID, "attachment_kind": "task", "name": a.FileName, "size": a.FileSize})
		}
		thread, err := s.repo.ThreadAttachments(ctx, id)
		if err != nil {
			return "", err
		}
		for _, a := range thread {
			out = append(out, map[string]any{"attachment_id": a.ID, "attachment_kind": "thread", "name": a.FileName, "size": a.FileSize})
		}
		data, _ := json.Marshal(out)
		return string(data), nil
	}
	if !safeTransferPath(args.Path) {
		return "", errors.New("choose a relative file in the computer Files directory")
	}
	executionID := llmcontracts.ArtifactExecutionIDFromContext(ctx)
	if args.Action == "from_computer" && executionID == "" {
		return "", errors.New("an execution is required to publish a computer file")
	}
	conn, err := s.Connection(ctx, id)
	if err != nil {
		return "", err
	}
	method := "GET"
	var data []byte
	if args.Action == "to_computer" {
		var attachmentPath string
		if args.Kind == "thread" {
			a, err := s.repo.ThreadAttachment(ctx, id, args.AttachmentID)
			if err != nil {
				return "", err
			}
			if a == nil {
				return "", errors.New("attachment does not belong to this task thread")
			}
			attachmentPath = a.FilePath
		} else if args.Kind == "" || args.Kind == "task" {
			a, err := repo.GetByIDForProject(ctx, args.AttachmentID, id)
			if err != nil {
				return "", err
			}
			if a == nil || a.TaskID != s.taskID {
				return "", errors.New("attachment does not belong to this task")
			}
			attachmentPath = a.FilePath
		} else {
			return "", errors.New("choose task or thread attachment_kind")
		}
		file, err := os.Open(attachmentPath)
		if err != nil {
			return "", errors.New("attachment unavailable")
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > computerTransferLimit {
			return "", errors.New("choose an attachment up to 25 MB")
		}
		data, err = io.ReadAll(io.LimitReader(file, computerTransferLimit+1))
		if err != nil {
			return "", err
		}
		if len(data) > computerTransferLimit {
			return "", errors.New("attachment exceeds 25 MB")
		}
		method = "PUT"
	} else if args.Action != "from_computer" {
		return "", errors.New("choose list, to_computer or from_computer")
	}
	req, err := http.NewRequestWithContext(ctx, method, conn.Endpoint+"/files?path="+url.QueryEscape("Files/"+args.Path), bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+conn.Token)
	res, err := s.client.Do(req)
	if err != nil {
		return "", errors.New("attachment transfer failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", errors.New("attachment transfer failed; check the path and choose a new filename")
	}
	if method == "PUT" {
		return "Attachment copied to computer", nil
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "application/octet-stream") {
		return "", errors.New("choose a file")
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, computerTransferLimit+1))
	if err != nil {
		return "", err
	}
	if len(data) > computerTransferLimit {
		return "", errors.New("file exceeds 25 MB")
	}
	if s.attachmentDirectory == "" {
		return "", errors.New("attachment storage is not configured")
	}
	out, err := publishGeneratedFile(ctx, repo, s.attachmentDirectory, id, executionID, args.Path, data)
	if err == nil && s.onFilePublished != nil {
		s.onFilePublished(ctx, id, executionID)
	}
	return out, err
}
