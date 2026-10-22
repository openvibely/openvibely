package handler

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/pkg/filecontent"
	"github.com/stretchr/testify/require"
)

func TestValidateFileUploadBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		data     []byte
		rejected bool
	}{
		{"screenshot.png", make([]byte, 11<<20), true},
		{"screenshot.png", make([]byte, 10<<20), false},
		{"text.txt", []byte(strings.Repeat("a", 4095) + "€" + strings.Repeat("a", int(filecontent.MaxTextBytes))), true},
		{"text.txt", []byte(strings.Repeat("a", int(filecontent.MaxTextBytes))), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("file", tc.name)
			require.NoError(t, err)
			_, err = part.Write(tc.data)
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			req := httptest.NewRequest("POST", "/", &body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			require.NoError(t, req.ParseMultipartForm(1<<20))
			defer req.MultipartForm.RemoveAll()
			err = validateFileUpload(req.MultipartForm.File["file"][0])
			if tc.rejected {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
