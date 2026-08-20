package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestUploadAttachment_TaskNotFound(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Post("/tasks/nonexistent-task/attachments").Execute()
	tc.Assert(rec).StatusCode(http.StatusNotFound)
}

func TestUploadAttachment_NoFiles(t *testing.T) {
	tc := NewTestContext(t)
	p := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).Build()

	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/attachments", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUploadAttachment_Success(t *testing.T) {
	tc := NewTestContext(t)
	p := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).Build()

	tmpDir := t.TempDir()
	origDir := uploadsDir
	SetUploadsDir(tmpDir)
	t.Cleanup(func() { SetUploadsDir(origDir) })

	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	fw, err := w.CreateFormFile("files", "hello.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	fw.Write([]byte("hello world"))
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/attachments", body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

type taskAttachmentUploadSurface struct {
	name         string
	wantSuccess  int
	wantNoUpload int
	upload       func(t *testing.T, tc *TestContext, projectID string, file taskAttachmentTestFile) (taskID string, rec *httptest.ResponseRecorder)
}

type taskAttachmentTestFile struct {
	name        string
	contentType string
	content     []byte
}

func taskAttachmentUploadSurfaces() []taskAttachmentUploadSurface {
	return []taskAttachmentUploadSurface{
		{
			name:         "task creation",
			wantSuccess:  http.StatusOK,
			wantNoUpload: http.StatusOK,
			upload: func(t *testing.T, tc *TestContext, projectID string, file taskAttachmentTestFile) (string, *httptest.ResponseRecorder) {
				t.Helper()
				title := "task attachment create surface"
				req := newTaskAttachmentMultipartRequest(t, http.MethodPost, "/tasks?project_id="+projectID, map[string]string{
					"title":    title,
					"category": "backlog",
					"priority": "2",
					"prompt":   "created with an attachment",
				}, file)
				req.Header.Set("HX-Request", "true")
				rec := httptest.NewRecorder()
				tc.echo.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					return "", rec
				}
				task, err := tc.taskRepo.GetByProjectAndTitle(context.Background(), projectID, title)
				if err != nil {
					t.Fatalf("get created task: %v", err)
				}
				if task == nil {
					t.Fatalf("created task %q not found", title)
				}
				return task.ID, rec
			},
		},
		{
			name:         "task edit",
			wantSuccess:  http.StatusOK,
			wantNoUpload: http.StatusOK,
			upload: func(t *testing.T, tc *TestContext, projectID string, file taskAttachmentTestFile) (string, *httptest.ResponseRecorder) {
				t.Helper()
				task := tc.CreateTask(projectID).
					WithTitle("task attachment edit surface").
					WithCategory(models.CategoryBacklog).
					WithPriority(2).
					Build()
				req := newTaskAttachmentMultipartRequest(t, http.MethodPut, "/tasks/"+task.ID, map[string]string{
					"title":    task.Title,
					"category": "backlog",
					"priority": "2",
					"prompt":   task.Prompt,
				}, file)
				req.Header.Set("HX-Request", "true")
				rec := httptest.NewRecorder()
				tc.echo.ServeHTTP(rec, req)
				return task.ID, rec
			},
		},
		{
			name:         "attachment endpoint",
			wantSuccess:  http.StatusOK,
			wantNoUpload: http.StatusBadRequest,
			upload: func(t *testing.T, tc *TestContext, projectID string, file taskAttachmentTestFile) (string, *httptest.ResponseRecorder) {
				t.Helper()
				task := tc.CreateTask(projectID).
					WithTitle("task attachment endpoint surface").
					WithCategory(models.CategoryBacklog).
					Build()
				req := newTaskAttachmentMultipartRequest(t, http.MethodPost, "/tasks/"+task.ID+"/attachments", nil, file)
				req.Header.Set("HX-Request", "true")
				rec := httptest.NewRecorder()
				tc.echo.ServeHTTP(rec, req)
				return task.ID, rec
			},
		},
	}
}

func newTaskAttachmentMultipartRequest(t *testing.T, method, target string, fields map[string]string, file taskAttachmentTestFile) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatalf("write field %s: %v", name, err)
		}
	}
	if file.name != "" {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="files"; filename="`+file.name+`"`)
		if file.contentType != "" {
			header.Set("Content-Type", file.contentType)
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := part.Write(file.content); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func withTaskAttachmentUploadsDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	origDir := uploadsDir
	SetUploadsDir(tmpDir)
	t.Cleanup(func() { SetUploadsDir(origDir) })
	return tmpDir
}

func TestTaskAttachmentUploadsPersistEquivalentRowsAcrossSurfaces(t *testing.T) {
	for _, surface := range taskAttachmentUploadSurfaces() {
		t.Run(surface.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().Build()
			uploadsRoot := withTaskAttachmentUploadsDir(t)

			file := taskAttachmentTestFile{
				name:    "../equivalent.txt",
				content: []byte("equivalent attachment content"),
			}
			taskID, rec := surface.upload(t, tc, project.ID, file)
			if rec.Code != surface.wantSuccess {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}

			attachments, err := tc.attachmentRepo.ListByTask(context.Background(), taskID)
			if err != nil {
				t.Fatalf("list attachments: %v", err)
			}
			if len(attachments) != 1 {
				t.Fatalf("expected 1 attachment, got %d", len(attachments))
			}
			att := attachments[0]
			wantPath := filepath.Join(uploadsRoot, taskID, "equivalent.txt")
			if att.TaskID != taskID || att.FileName != "equivalent.txt" || att.FilePath != wantPath || att.MediaType != "application/octet-stream" || att.FileSize != int64(len(file.content)) {
				t.Fatalf("unexpected attachment row: %+v want task=%s file=%s type=%s size=%d", att, taskID, wantPath, "application/octet-stream", len(file.content))
			}
			stored, err := os.ReadFile(att.FilePath)
			if err != nil {
				t.Fatalf("read stored file: %v", err)
			}
			if !bytes.Equal(stored, file.content) {
				t.Fatalf("stored content = %q, want %q", stored, file.content)
			}
		})
	}
}

func TestTaskAttachmentUploadsSkipOversizedFilesAcrossSurfaces(t *testing.T) {
	for _, surface := range taskAttachmentUploadSurfaces() {
		t.Run(surface.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().Build()
			withTaskAttachmentUploadsDir(t)

			file := taskAttachmentTestFile{
				name:        "too-large.txt",
				contentType: "text/plain",
				content:     bytes.Repeat([]byte("x"), maxUploadSize+1),
			}
			taskID, rec := surface.upload(t, tc, project.ID, file)
			if rec.Code != surface.wantNoUpload {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if taskID == "" {
				t.Fatalf("expected task ID")
			}

			attachments, err := tc.attachmentRepo.ListByTask(context.Background(), taskID)
			if err != nil {
				t.Fatalf("list attachments: %v", err)
			}
			if len(attachments) != 0 {
				t.Fatalf("expected no attachments, got %+v", attachments)
			}
		})
	}
}

func TestTaskAttachmentUploadsCleanPartialFilesOnCopyFailureAcrossSurfaces(t *testing.T) {
	origCopy := taskAttachmentCopy
	taskAttachmentCopy = func(dst io.Writer, src io.Reader) (int64, error) {
		if _, err := dst.Write([]byte("partial")); err != nil {
			return 0, err
		}
		return 0, errors.New("forced copy failure")
	}
	t.Cleanup(func() { taskAttachmentCopy = origCopy })

	for _, surface := range taskAttachmentUploadSurfaces() {
		t.Run(surface.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().Build()
			uploadsRoot := withTaskAttachmentUploadsDir(t)

			file := taskAttachmentTestFile{
				name:        "copy-failure.txt",
				contentType: "text/plain",
				content:     []byte("copy should fail"),
			}
			taskID, rec := surface.upload(t, tc, project.ID, file)
			if rec.Code != surface.wantNoUpload {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if taskID == "" {
				t.Fatalf("expected task ID")
			}
			assertNoTaskAttachmentsOrFile(t, tc, taskID, filepath.Join(uploadsRoot, taskID, "copy-failure.txt"))
		})
	}
}

func TestTaskAttachmentUploadsCleanFilesOnRepositoryFailureAcrossSurfaces(t *testing.T) {
	for _, surface := range taskAttachmentUploadSurfaces() {
		t.Run(surface.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().Build()
			uploadsRoot := withTaskAttachmentUploadsDir(t)
			if _, err := tc.db.ExecContext(context.Background(), `
				CREATE TRIGGER fail_task_attachment_insert
				BEFORE INSERT ON task_attachments
				BEGIN
					SELECT RAISE(ABORT, 'forced attachment insert failure');
				END;
			`); err != nil {
				t.Fatalf("create failure trigger: %v", err)
			}

			file := taskAttachmentTestFile{
				name:        "repository-failure.txt",
				contentType: "text/plain",
				content:     []byte("repository should fail"),
			}
			taskID, rec := surface.upload(t, tc, project.ID, file)
			if rec.Code != surface.wantNoUpload {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if taskID == "" {
				t.Fatalf("expected task ID")
			}
			assertNoTaskAttachmentsOrFile(t, tc, taskID, filepath.Join(uploadsRoot, taskID, "repository-failure.txt"))
		})
	}
}

func assertNoTaskAttachmentsOrFile(t *testing.T, tc *TestContext, taskID, path string) {
	t.Helper()
	attachments, err := tc.attachmentRepo.ListByTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	if len(attachments) != 0 {
		t.Fatalf("expected no attachments, got %+v", attachments)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected partial file %s to be removed, stat err=%v", path, err)
	}
}

func TestDeleteAttachment_NotFound(t *testing.T) {
	tc := NewTestContext(t)
	rec := tc.HTTP().Delete("/attachments/nonexistent-attachment").Execute()
	tc.Assert(rec).StatusCode(http.StatusNotFound)
}

func TestDeleteAttachment_Success(t *testing.T) {
	tc := NewTestContext(t)
	p := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).Build()

	tmpDir := t.TempDir()
	origDir := uploadsDir
	SetUploadsDir(tmpDir)
	t.Cleanup(func() { SetUploadsDir(origDir) })

	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "test.txt",
		FilePath:  filepath.Join(tmpDir, "test.txt"),
		MediaType: "text/plain",
		FileSize:  4,
	}
	if err := tc.attachmentRepo.Create(context.Background(), attachment); err != nil {
		t.Fatalf("create attachment: %v", err)
	}

	rec := tc.HTTP().Delete("/attachments/" + attachment.ID).Execute()
	tc.Assert(rec).StatusCode(http.StatusOK)
}
