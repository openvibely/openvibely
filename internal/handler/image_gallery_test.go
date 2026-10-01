package handler

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestImageGalleryAttachmentAccess(t *testing.T) {
	tc := NewTestContext(t)
	useTempUploadsDir(t)
	p := tc.CreateProject().Build()
	other := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).Build()
	path := filepath.Join(uploadsDir, "image.png")
	require.NoError(t, os.WriteFile(path, []byte("image"), 0600))
	att := &models.Attachment{TaskID: task.ID, FileName: "image.png", FilePath: path, MediaType: "image/png"}
	require.NoError(t, tc.handler.attachmentRepo.Create(context.Background(), att))
	rec := tc.HTTP().Get("/attachments/" + att.ID + "/download?project_id=" + p.ID).Execute()
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image", rec.Body.String())
	rec = tc.HTTP().Get("/attachments/" + att.ID + "/download?project_id=" + other.ID).Execute()
	require.Equal(t, http.StatusNotFound, rec.Code)
	session := "01234567890123456789012345678901"
	dir := filepath.Join(uploadsDir, "chat", "pending", session)
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "draft.png"), []byte("draft"), 0600))
	rec = tc.HTTP().Get("/chat/attachments/pending/" + session + "/draft.png").Execute()
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "draft", rec.Body.String())
	for _, url := range []string{"/chat/attachments/pending/invalid/draft.png", "/chat/attachments/pending/" + session + "/%2e%2e%2fimage.png"} {
		rec = tc.HTTP().Get(url).Execute()
		require.Equal(t, http.StatusNotFound, rec.Code)
	}
}
