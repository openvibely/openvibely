package attachment

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestModelInputsPreserveDirectImages(t *testing.T) {
	for _, ext := range []string{"png", "jpg", "jpeg", "gif", "webp"} {
		t.Run(ext, func(t *testing.T) {
			name := "original." + ext
			path := filepath.Join(t.TempDir(), name)
			original := []byte("original image bytes")
			require.NoError(t, os.WriteFile(path, original, 0600))
			got, err := ModelInputs(context.Background(), []models.Attachment{{FileName: name, FilePath: path}})
			require.NoError(t, err)
			require.Len(t, got, 1)
			media := "image/" + ext
			if ext == "jpg" {
				media = "image/jpeg"
			}
			require.Equal(t, media, got[0].MediaType)
			require.Equal(t, original, got[0].Data)
		})
	}
}

func TestModelInputsOtherFormatsNeedNoReadOrConverter(t *testing.T) {
	for _, ext := range []string{"pdf", "docx", "pptx", "xlsx", "xls", "xlsb", "ods", "doc", "ppt", "txt", "svg", "mp4", "mp3", "avif", "bmp", "heic", "zip"} {
		t.Run(ext, func(t *testing.T) {
			att := models.Attachment{FileName: "file." + ext, FilePath: filepath.Join(t.TempDir(), "missing."+ext)}
			got, err := ModelInputs(context.Background(), []models.Attachment{att})
			require.NoError(t, err, "references must not open or convert the original file")
			require.Len(t, got, 1)
			require.Equal(t, "text/plain", got[0].MediaType)
			require.Equal(t, Reference(att), string(got[0].Data))
		})
	}
}

func TestModelInputsReadLimitsAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.png")
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, file.Truncate((10<<20)+1))
	require.NoError(t, file.Close())
	_, err = ModelInputs(context.Background(), []models.Attachment{{FilePath: path}})
	require.ErrorContains(t, err, "limit")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = ModelInputs(ctx, []models.Attachment{{FilePath: path}})
	require.ErrorIs(t, err, context.Canceled)
}
