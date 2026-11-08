package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
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
