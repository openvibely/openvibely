package handler

import (
	"bytes"
	"context"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/stretchr/testify/require"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestRemoteUploadDuplicateNames(t *testing.T) {
	old := uploadsDir
	uploadsDir = t.TempDir()
	t.Cleanup(func() { uploadsDir = old })
	h, e := setupHubHandler(t)
	srv, _, owner := fakeRemoteOV(t, "secret")
	project := createProject(t, owner, "Project 1")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	sid := savedServerID(t, h)
	require.Eventually(t, func() bool {
		return h.remoteSignedIn(context.Background(), sid) && !h.remoteDown(context.Background(), sid)
	}, 5*time.Second, 50*time.Millisecond)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	names := []string{"notes.txt", "notes.txt", "notes-1.txt"}
	contents := []string{"first file", "second file", "third file"}
	for i, data := range contents {
		part, err := writer.CreateFormFile("files", names[i])
		require.NoError(t, err)
		_, err = part.Write([]byte(data))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	require.NoError(t, req.ParseMultipartForm(1<<20))
	defer req.MultipartForm.RemoveAll()
	files := requestUploads(req.MultipartForm.File["files"])
	local, err := h.stageUploads(context.Background(), "", "local-project", files)
	require.NoError(t, err)
	require.Len(t, local, 3)
	for i, f := range local {
		require.Equal(t, names[i], f.Name)
	}
	staged, err := h.stageUploads(context.Background(), "", remoteid.ID(sid, project.ID), files)
	require.NoError(t, err)
	require.Equal(t, []string{"notes.txt", "notes-1.txt", "notes-1-1.txt"}, []string{staged[0].Name, staged[1].Name, staged[2].Name})
	result, err := actPendingUpload(h, context.Background(), pendingUploadArgs{ProjectID: remoteid.ID(sid, project.ID), SessionID: generateSessionID(), Files: staged})
	require.NoError(t, err)
	require.Len(t, result.Attachments, 3)
	for i, file := range result.Attachments {
		require.Equal(t, staged[i].Name, file.Filename)
		var chunk fileChunk
		err = h.remoteHub().Act(context.Background(), sid, "file.read", fileReadArgs{Kind: fileKindPending, ID: result.SessionID + "/" + file.Filename}, &chunk)
		require.NoError(t, err)
		require.Equal(t, contents[i], string(chunk.Data))
	}
}
