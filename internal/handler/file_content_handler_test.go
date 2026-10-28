package handler

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	llmattachment "github.com/openvibely/openvibely/internal/llm/attachment"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestFilePreviewRoutesPreserveOriginalAndProjectScope(t *testing.T) {
	tc := NewStoreTestContext(t)
	useTempUploadsDir(t)
	project := tc.CreateProject().Build()
	other := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).Build()
	for _, sample := range []struct{ name, content, marker string }{{"page.html", "<script>window.localDemo=1</script><h1>Hello</h1>", "sandbox=\"allow-scripts\""}, {"data.json", `{"amount":12345678901234567890}`, "12345678901234567890"}, {"report.pdf", "%PDF-1.4", "data-pdf-viewer"}, {"table.xlsx", "PK", "data-spreadsheet-url"}, {"old.doc", "binary", "Preview not available"}} {
		t.Run(sample.name, func(t *testing.T) {
			path := filepath.Join(uploadsDir, sample.name)
			require.NoError(t, os.WriteFile(path, []byte(sample.content), 0600))
			att := &models.Attachment{TaskID: task.ID, FileName: sample.name, FilePath: path, FileSize: int64(len(sample.content))}
			require.NoError(t, tc.handler.attachmentRepo.Create(context.Background(), att))
			base := "/attachments/" + att.ID + "/download?project_id=" + project.ID
			response := tc.HTTP().Get(base + "&view=1").Execute()
			require.Equal(t, 200, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), sample.marker)
			response = tc.HTTP().Get(base + "&download=1").Execute()
			require.Equal(t, sample.content, response.Body.String())
			require.Contains(t, response.Header().Get("Content-Disposition"), "attachment")
			response = tc.HTTP().Get("/attachments/" + att.ID + "/download?project_id=" + other.ID + "&view=1").Execute()
			require.Equal(t, 404, response.Code)
			if sample.name == "page.html" {
				response = tc.HTTP().Get(base + "&raw=1").Execute()
				require.Contains(t, response.Header().Get("Content-Security-Policy"), "sandbox allow-scripts")
				require.Contains(t, response.Header().Get("Content-Security-Policy"), "connect-src 'none'")
			}
		})
	}
}
func TestFileUploadTextLimitAndVideoAdmission(t *testing.T) {
	tc := NewStoreTestContext(t)
	useTempUploadsDir(t)
	for _, tcFile := range []struct {
		name   string
		size   int
		status int
	}{{"too-large.txt", 256*1024 + 1, 400}, {"accepted.txt", 256 * 1024, 200}, {"clip.mp4", 26 * 1024 * 1024, 200}, {"large.png", 26 * 1024 * 1024, 413}} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("files", tcFile.name)
		require.NoError(t, err)
		_, err = part.Write([]byte(strings.Repeat("x", tcFile.size)))
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		request := httptest.NewRequest(http.MethodPost, "/chat/attachments", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		recorder := httptest.NewRecorder()
		tc.echo.ServeHTTP(recorder, request)
		require.Equal(t, tcFile.status, recorder.Code, recorder.Body.String())
	}
}

func TestChatBinaryAttachmentsReachModelPreparation(t *testing.T) {
	dir := t.TempDir()
	var files []chatAttachmentModelContextFile
	for _, name := range []string{"document.pdf", "workbook.xlsx", "slides.pptx", "drawing.svg", "clip.mp4", "voice.mp3"} {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte("original binary bytes"), 0600))
		files = append(files, chatAttachmentModelContextFile{FileName: name, FilePath: path, FileSize: 21, MediaType: mediaTypeFromExtension(name)})
	}
	text, attachments, err := buildChatAttachmentModelContext(files, chatAttachmentModelContextOptions{})
	require.NoError(t, err)
	require.NotContains(t, text, "original binary bytes", "binary bytes must never be injected into the prompt")
	require.Contains(t, text, "document.pdf")
	require.Len(t, attachments, len(files))
	for i, att := range attachments {
		require.Equal(t, files[i].FilePath, att.FilePath)
	}
}

func TestGeneratedFileRoutesAndFilesPanelAreProjectScoped(t *testing.T) {
	tc := NewStoreTestContext(t)
	useTempUploadsDir(t)
	p := tc.CreateProject().Build()
	other := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).Build()
	execution := tc.CreateExecution(task.ID, "").Build()
	path := filepath.Join(uploadsDir, "result.html")
	require.NoError(t, os.WriteFile(path, []byte("<button>Result</button>"), 0600))
	f := &models.GeneratedFile{ProjectID: p.ID, ExecutionID: execution.ID, FileName: "result.html", FilePath: path, Display: "canvas", MediaType: "text/html", FileSize: 23}
	require.NoError(t, tc.handler.attachmentRepo.CreateGeneratedFile(context.Background(), f))
	response := tc.HTTP().Get("/files/" + f.ID + "?project_id=" + other.ID + "&metadata=1").Execute()
	require.Equal(t, 404, response.Code)
	response = tc.HTTP().Get("/files/" + f.ID + "?project_id=" + p.ID + "&metadata=1").Execute()
	require.Equal(t, 200, response.Code)
	require.NotContains(t, response.Body.String(), path)
	require.Contains(t, response.Body.String(), "canvas")
	response = tc.HTTP().Get("/files?project_id=" + p.ID + "&task_id=" + task.ID).Execute()
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), "result.html")
	require.Contains(t, response.Body.String(), "Attach to message")
	response = tc.HTTP().Get("/files?project_id=" + other.ID).Execute()
	require.NotContains(t, response.Body.String(), "result.html")
}

func TestSteeringDocumentReferenceUsesCommittedPath(t *testing.T) {
	tc := NewStoreTestContext(t)
	useTempUploadsDir(t)
	session := "reference-test"
	source := filepath.Join(uploadsDir, "chat", "pending", session, "report.pdf")
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0700))
	require.NoError(t, os.WriteFile(source, []byte("%PDF-original"), 0600))
	h := tc.handler
	text, files, err := h.previewSteeringAttachments(session, "exec", models.ProviderOpenAI)
	require.NoError(t, err)
	require.Len(t, files, 1)
	target := filepath.Join(uploadsDir, "chat", "exec", "steering", session, "report.pdf")
	require.Contains(t, text, target)
	require.NotContains(t, text, source)
	require.Equal(t, source, files[0].FilePath)
	require.Equal(t, target, files[0].ReferencePath)
	parts, err := llmattachment.ModelInputs(context.Background(), files)
	require.NoError(t, err)
	require.Contains(t, string(parts[0].Data), target)
	require.NotContains(t, string(parts[0].Data), "%PDF-original")
}

func TestFileContentServesOnlyRequestedPrecomputedSpreadsheetStrings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workbook.xlsx")
	indexPath := path + ".spreadsheet-strings.json"
	require.NoError(t, os.WriteFile(indexPath, []byte(`{"0":"visible","6001":"cold high-index value"}`), 0600))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/file?spreadsheet-strings=1", strings.NewReader(`[6001]`))
	request.Header.Set("Content-Type", "application/json")
	ctx := echo.New().NewContext(request, recorder)
	require.NoError(t, serveFileContent(ctx, path, filepath.Base(path)))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"6001":"cold high-index value"}`, recorder.Body.String())
	require.Equal(t, "private, no-store", recorder.Header().Get("Cache-Control"))
}

func TestFileContentBuildsMissingSpreadsheetIndexAsynchronously(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.xlsx")
	require.NoError(t, os.WriteFile(path, []byte("not a workbook"), 0600))
	request := func() (int, error) {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/file?spreadsheet-strings=1", strings.NewReader(`[6001]`))
		ctx := echo.New().NewContext(req, recorder)
		return recorder.Code, serveFileContent(ctx, path, filepath.Base(path))
	}
	status, err := request()
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status, "a missing preview index must not scan the workbook in the request")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err = request()
		if httpErr, ok := err.(*echo.HTTPError); ok && httpErr.Code == http.StatusServiceUnavailable {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Fail(t, "failed background indexing should surface an unavailable response instead of retrying the SST scan")
}

func TestFilePreviewReportsUnsupportedEncoding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.txt")
	require.NoError(t, os.WriteFile(path, []byte("caf\xe9"), 0600))
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/file?view=1", nil), rec)
	require.NoError(t, serveFileContent(c, path, "legacy.txt"))
	require.Contains(t, rec.Body.String(), "Unsupported text encoding")
	rec = httptest.NewRecorder()
	c = e.NewContext(httptest.NewRequest(http.MethodGet, "/file?download=1", nil), rec)
	require.NoError(t, serveFileContent(c, path, "legacy.txt"))
	require.Equal(t, []byte("caf\xe9"), rec.Body.Bytes())
}
