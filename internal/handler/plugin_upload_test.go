package handler

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPluginPackageFolderUpload(t *testing.T) {
	env := setupPluginPages(t)
	manifest := `{"schema_version":1,"id":"test.upload","name":"Uploaded","version":"1.0.0","pages":[{"id":"home","title":"Home","entry":"assets/index.html"}]}`
	for _, tc := range []struct {
		name     string
		paths    []string
		contents []string
		ok       bool
	}{
		{"valid", []string{"bundle/plugin.json", "bundle/assets/index.html"}, []string{manifest, "<html>Hello</html>"}, true},
		{"traversal", []string{"bundle/plugin.json", "bundle/../escape"}, []string{manifest, "bad"}, false},
		{"absolute", []string{"/bundle/plugin.json"}, []string{manifest}, false},
		{"backslash", []string{"bundle/..\\escape"}, []string{"bad"}, false},
		{"duplicate", []string{"bundle/plugin.json", "bundle/plugin.json"}, []string{manifest, manifest}, false},
		{"missing manifest", []string{"bundle/index.html"}, []string{"html"}, false},
		{"invalid manifest", []string{"bundle/plugin.json"}, []string{"{}"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			w := multipart.NewWriter(&body)
			require.NoError(t, w.WriteField("open_install", "true"))
			for i, p := range tc.paths {
				f, err := w.CreateFormFile("files", "upload")
				require.NoError(t, err)
				_, err = f.Write([]byte(tc.contents[i]))
				require.NoError(t, err)
				require.NoError(t, w.WriteField("paths", p))
			}
			require.NoError(t, w.Close())
			rec := env.do(t, http.MethodPost, "/settings/plugin-pages/upload?project_id="+env.projA.ID, body.String(), map[string]string{"Content-Type": w.FormDataContentType(), "HX-Request": "true"})
			require.Equal(t, 200, rec.Code)
			if tc.ok {
				require.Contains(t, rec.Header().Get("HX-Trigger"), "Installed Uploaded")
			} else {
				require.Contains(t, rec.Body.String(), `id="plugin-install-error"`)
			}
		})
	}
}

func TestPluginUploadFileCountLimits(t *testing.T) {
	for _, count := range []int{501, 2000, 2001} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			env := setupPluginPages(t)
			var body bytes.Buffer
			w := multipart.NewWriter(&body)
			require.NoError(t, w.WriteField("open_install", "true"))
			for i := 0; i < count; i++ {
				name := fmt.Sprintf("bundle/assets/file%d.html", i)
				data := "x"
				if i == 0 {
					name = "bundle/plugin.json"
					data = `{"schema_version":1,"id":"test.many","name":"Many","version":"1.0.0","pages":[{"id":"home","title":"Home","entry":"assets/file1.html"}]}`
				}
				f, err := w.CreateFormFile("files", "upload")
				require.NoError(t, err)
				_, err = f.Write([]byte(data))
				require.NoError(t, err)
				require.NoError(t, w.WriteField("paths", name))
			}
			require.NoError(t, w.Close())
			rec := env.do(t, http.MethodPost, "/settings/plugin-pages/upload?project_id="+env.projA.ID, body.String(), map[string]string{"Content-Type": w.FormDataContentType(), "HX-Request": "true"})
			require.Equal(t, 200, rec.Code)
			if count <= 2000 {
				require.Contains(t, rec.Header().Get("HX-Trigger"), "Installed Many", rec.Body.String())
			} else {
				require.Contains(t, rec.Body.String(), "too many files: maximum 2000")
				require.Contains(t, rec.Body.String(), `id="plugin-install-error"`)
			}
		})
	}
}

func TestPluginUploadByteLimit(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			var prefix bytes.Buffer
			w := multipart.NewWriter(&prefix)
			_, err := w.CreateFormFile("files", "large.bin")
			require.NoError(t, err)
			header := prefix.String()
			prefix.Reset()
			require.NoError(t, w.Close())
			// Stream the payload without constructing a second multipart copy.
			req := httptest.NewRequest(http.MethodPost, "/", io.MultiReader(strings.NewReader(header), io.LimitReader(zeroUploadReader{}, int64(64<<20)+int64(extra)), strings.NewReader(prefix.String())))
			req.Header.Set("Content-Type", w.FormDataContentType())
			_, _, err = readPluginUpload(req, t.TempDir())
			if extra == 0 {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "package files exceed 64 MiB")
			}
		})
	}
}

type zeroUploadReader struct{}

func (zeroUploadReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
