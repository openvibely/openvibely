package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	var files []taskAttachmentTestFile
	if file.name != "" {
		files = append(files, file)
	}
	return newTaskAttachmentMultipartRequestWithFiles(t, method, target, fields, files)
}

func newTaskAttachmentMultipartRequestWithFiles(t *testing.T, method, target string, fields map[string]string, files []taskAttachmentTestFile) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatalf("write field %s: %v", name, err)
		}
	}
	for _, file := range files {
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

func TestTaskAttachmentUploadsAllowValidMultiFileBodyAboveSingleFileRequestCapAcrossSurfaces(t *testing.T) {
	for _, tcCase := range []struct {
		name    string
		request func(t *testing.T, tc *TestContext, projectID string, files []taskAttachmentTestFile) (*http.Request, func() string)
	}{
		{
			name: "task creation",
			request: func(t *testing.T, tc *TestContext, projectID string, files []taskAttachmentTestFile) (*http.Request, func() string) {
				t.Helper()
				title := "task attachment create multi file above single cap"
				req := newTaskAttachmentMultipartRequestWithFiles(t, http.MethodPost, "/tasks?project_id="+projectID, map[string]string{
					"title":    title,
					"category": "backlog",
					"priority": "2",
					"prompt":   "created with multiple attachments",
				}, files)
				req.Header.Set("HX-Request", "true")
				return req, func() string {
					task, err := tc.taskRepo.GetByProjectAndTitle(context.Background(), projectID, title)
					if err != nil {
						t.Fatalf("get created task: %v", err)
					}
					if task == nil {
						t.Fatalf("created task %q not found", title)
					}
					return task.ID
				}
			},
		},
		{
			name: "task edit",
			request: func(t *testing.T, tc *TestContext, projectID string, files []taskAttachmentTestFile) (*http.Request, func() string) {
				t.Helper()
				task := tc.CreateTask(projectID).
					WithTitle("task attachment edit multi file above single cap").
					WithCategory(models.CategoryBacklog).
					WithPriority(2).
					Build()
				req := newTaskAttachmentMultipartRequestWithFiles(t, http.MethodPut, "/tasks/"+task.ID, map[string]string{
					"title":    task.Title,
					"category": "backlog",
					"priority": "2",
					"prompt":   task.Prompt,
				}, files)
				req.Header.Set("HX-Request", "true")
				return req, func() string { return task.ID }
			},
		},
		{
			name: "attachment endpoint",
			request: func(t *testing.T, tc *TestContext, projectID string, files []taskAttachmentTestFile) (*http.Request, func() string) {
				t.Helper()
				task := tc.CreateTask(projectID).
					WithTitle("task attachment endpoint multi file above single cap").
					WithCategory(models.CategoryBacklog).
					Build()
				req := newTaskAttachmentMultipartRequestWithFiles(t, http.MethodPost, "/tasks/"+task.ID+"/attachments", nil, files)
				req.Header.Set("HX-Request", "true")
				return req, func() string { return task.ID }
			},
		},
	} {
		t.Run(tcCase.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().Build()
			uploadsRoot := withTaskAttachmentUploadsDir(t)
			files := []taskAttachmentTestFile{
				{name: "first.bin", contentType: "application/octet-stream", content: bytes.Repeat([]byte("a"), 6<<20)},
				{name: "second.bin", contentType: "application/octet-stream", content: bytes.Repeat([]byte("b"), 6<<20)},
			}
			req, taskIDAfterUpload := tcCase.request(t, tc, project.ID, files)
			if req.ContentLength <= browserAttachmentRequestLimit(maxUploadSize) {
				t.Fatalf("test request length %d should exceed old one-file cap %d", req.ContentLength, browserAttachmentRequestLimit(maxUploadSize))
			}
			if req.ContentLength > attachmentRequestLimit(maxUploadSize, maxTaskAttachmentFilesPerRequest) {
				t.Fatalf("test request length %d should fit task request cap %d", req.ContentLength, attachmentRequestLimit(maxUploadSize, maxTaskAttachmentFilesPerRequest))
			}

			rec := httptest.NewRecorder()
			tc.echo.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}

			taskID := taskIDAfterUpload()
			attachments, err := tc.attachmentRepo.ListByTask(context.Background(), taskID)
			if err != nil {
				t.Fatalf("list attachments: %v", err)
			}
			if len(attachments) != len(files) {
				t.Fatalf("expected %d attachments, got %d: %+v", len(files), len(attachments), attachments)
			}
			expectedSizes := map[string]int64{
				"first.bin":  int64(len(files[0].content)),
				"second.bin": int64(len(files[1].content)),
			}
			for _, att := range attachments {
				wantSize, ok := expectedSizes[att.FileName]
				if !ok {
					t.Fatalf("unexpected attachment %q", att.FileName)
				}
				if att.FileSize != wantSize {
					t.Fatalf("attachment %s size=%d want=%d", att.FileName, att.FileSize, wantSize)
				}
				if _, err := os.Stat(filepath.Join(uploadsRoot, taskID, att.FileName)); err != nil {
					t.Fatalf("expected stored file %s: %v", att.FileName, err)
				}
			}
		})
	}
}

func TestTaskAttachmentUploadsRejectOversizedFilesAcrossSurfaces(t *testing.T) {
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
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}

			if taskID == "" {
				tasks, err := tc.taskRepo.ListByProject(context.Background(), project.ID, "")
				if err != nil {
					t.Fatalf("list tasks: %v", err)
				}
				if len(tasks) != 0 {
					t.Fatalf("expected no task to be created, got %+v", tasks)
				}
				return
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

func TestTaskCreateAndEditAttachmentFormsRejectOversizedMultipartBeforeMutation(t *testing.T) {
	for _, tcCase := range []struct {
		name   string
		target func(t *testing.T, tc *TestContext, projectID string) (method string, path string, assertNoMutation func())
	}{
		{
			name: "create",
			target: func(t *testing.T, tc *TestContext, projectID string) (string, string, func()) {
				t.Helper()
				return http.MethodPost, "/tasks?project_id=" + projectID, func() {
					tasks, err := tc.taskRepo.ListByProject(context.Background(), projectID, "")
					if err != nil {
						t.Fatalf("list tasks: %v", err)
					}
					if len(tasks) != 0 {
						t.Fatalf("expected no task to be created, got %+v", tasks)
					}
				}
			},
		},
		{
			name: "edit",
			target: func(t *testing.T, tc *TestContext, projectID string) (string, string, func()) {
				t.Helper()
				task := tc.CreateTask(projectID).WithTitle("original title").WithCategory(models.CategoryBacklog).Build()
				return http.MethodPut, "/tasks/" + task.ID, func() {
					updated, err := tc.taskRepo.GetByID(context.Background(), task.ID)
					if err != nil {
						t.Fatalf("get task: %v", err)
					}
					if updated.Title != "original title" {
						t.Fatalf("expected task title to remain unchanged, got %q", updated.Title)
					}
					attachments, err := tc.attachmentRepo.ListByTask(context.Background(), task.ID)
					if err != nil {
						t.Fatalf("list attachments: %v", err)
					}
					if len(attachments) != 0 {
						t.Fatalf("expected no attachments, got %+v", attachments)
					}
				}
			},
		},
	} {
		t.Run(tcCase.name, func(t *testing.T) {
			tmpMultipartDir := t.TempDir()
			t.Setenv("TMPDIR", tmpMultipartDir)
			tc := NewTestContext(t)
			project := tc.CreateProject().Build()
			withTaskAttachmentUploadsDir(t)
			method, target, assertNoMutation := tcCase.target(t, tc, project.ID)
			req, body, totalSize := newSizedMultipartUploadRequestWithFilePrefix(t, method, target, map[string]string{
				"title":    "mutated title",
				"category": "backlog",
				"priority": "2",
				"prompt":   "mutated prompt",
			}, "files", "too-large.txt", "text/plain", 25<<20, invalidMultipartBoundaryPayloadPrefix(), false)
			body.limitReadChunkSize(1)
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			tc.echo.ServeHTTP(rec, req)

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("expected 413, got %d; body=%s", rec.Code, rec.Body.String())
			}
			maxRead := browserAttachmentRequestLimit(maxUploadSize) + 1
			if body.bytesRead > maxRead {
				t.Fatalf("read %d bytes, want <= %d", body.bytesRead, maxRead)
			}
			if body.bytesRead >= totalSize {
				t.Fatalf("read full oversized body: read=%d total=%d", body.bytesRead, totalSize)
			}
			assertNoMutation()
			assertDirEmpty(t, tmpMultipartDir)
		})
	}
}

func TestUploadAttachment_OversizedMultipartRequestIsBoundedBeforePersistence(t *testing.T) {
	tmpMultipartDir := t.TempDir()
	t.Setenv("TMPDIR", tmpMultipartDir)
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	uploadsRoot := withTaskAttachmentUploadsDir(t)

	req, body, totalSize := newSizedMultipartUploadRequestWithFilePrefix(t, http.MethodPost, "/tasks/"+task.ID+"/attachments", nil, "files", "too-large.txt", "text/plain", 25<<20, invalidMultipartBoundaryPayloadPrefix(), false)
	body.limitReadChunkSize(1)
	rec := httptest.NewRecorder()
	tc.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d; body=%s", rec.Code, rec.Body.String())
	}
	maxRead := browserAttachmentRequestLimit(maxUploadSize) + 1
	if body.bytesRead > maxRead {
		t.Fatalf("read %d bytes, want <= %d", body.bytesRead, maxRead)
	}
	if body.bytesRead >= totalSize {
		t.Fatalf("read full oversized body: read=%d total=%d", body.bytesRead, totalSize)
	}

	attachments, err := tc.attachmentRepo.ListByTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	if len(attachments) != 0 {
		t.Fatalf("expected no attachments, got %+v", attachments)
	}
	if _, err := os.Stat(filepath.Join(uploadsRoot, task.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected no task attachment directory, stat err=%v", err)
	}
	assertDirEmpty(t, tmpMultipartDir)
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

type countingReadCloser struct {
	r         io.Reader
	bytesRead int64
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.bytesRead += int64(n)
	return n, err
}

func (r *countingReadCloser) Close() error { return nil }

func (r *countingReadCloser) limitReadChunkSize(max int) {
	r.r = &maxChunkReader{r: r.r, max: max}
}

type maxChunkReader struct {
	r   io.Reader
	max int
}

func (r *maxChunkReader) Read(p []byte) (int, error) {
	if len(p) > r.max {
		p = p[:r.max]
	}
	return r.r.Read(p)
}

type repeatedByteReader struct {
	remaining int64
}

func (r *repeatedByteReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	for i := range p {
		p[i] = 'x'
	}
	r.remaining -= int64(len(p))
	return len(p), nil
}

func newSizedMultipartUploadRequest(t *testing.T, method, target string, fields map[string]string, fileField, filename, contentType string, fileSize int64, setContentLength bool) (*http.Request, *countingReadCloser, int64) {
	t.Helper()
	return newSizedMultipartUploadRequestWithFilePrefix(t, method, target, fields, fileField, filename, contentType, fileSize, nil, setContentLength)
}

func newSizedMultipartUploadRequestWithFilePrefix(t *testing.T, method, target string, fields map[string]string, fileField, filename, contentType string, fileSize int64, filePrefix []byte, setContentLength bool) (*http.Request, *countingReadCloser, int64) {
	t.Helper()
	const boundary = "openvibely-issue-748-boundary"
	if int64(len(filePrefix)) > fileSize {
		t.Fatalf("file prefix length %d exceeds file size %d", len(filePrefix), fileSize)
	}
	var prefix bytes.Buffer
	for name, value := range fields {
		fmt.Fprintf(&prefix, "--%s\r\nContent-Disposition: form-data; name=%q\r\n\r\n%s\r\n", boundary, name, value)
	}
	fmt.Fprintf(&prefix, "--%s\r\nContent-Disposition: form-data; name=%q; filename=%q\r\n", boundary, fileField, filename)
	if contentType != "" {
		fmt.Fprintf(&prefix, "Content-Type: %s\r\n", contentType)
	}
	prefix.WriteString("\r\n")
	suffix := []byte("\r\n--" + boundary + "--\r\n")
	totalSize := int64(prefix.Len()) + fileSize + int64(len(suffix))
	body := &countingReadCloser{r: io.MultiReader(bytes.NewReader(prefix.Bytes()), bytes.NewReader(filePrefix), &repeatedByteReader{remaining: fileSize - int64(len(filePrefix))}, bytes.NewReader(suffix))}
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	if setContentLength {
		req.ContentLength = totalSize
	}
	return req, body, totalSize
}

func invalidMultipartBoundaryPayloadPrefix() []byte {
	const boundary = "openvibely-issue-748-boundary"
	return []byte("\r\n--" + boundary + "X\r\n\r\n")
}

func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected %s to be empty, found %d entries", dir, len(entries))
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
