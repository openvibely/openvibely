package attachment

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

const DefaultMaxImageBytes int64 = 10 * 1024 * 1024 // 10MB

// Preprocess normalizes attachments and applies lightweight guardrails.
func Preprocess(in []models.Attachment) ([]models.Attachment, error) {
	if len(in) == 0 {
		return nil, nil
	}

	out := make([]models.Attachment, len(in))
	copy(out, in)

	for i := range out {
		out[i].FileName = strings.TrimSpace(out[i].FileName)
		out[i].MediaType = strings.TrimSpace(out[i].MediaType)

		if out[i].FilePath != "" {
			if abs, err := filepath.Abs(out[i].FilePath); err == nil {
				out[i].FilePath = abs
			}
		}

		if out[i].MediaType == "" && out[i].FilePath != "" {
			out[i].MediaType = detectMediaType(out[i].FilePath)
		}

		size := out[i].FileSize
		if out[i].FilePath != "" {
			if st, err := os.Stat(out[i].FilePath); err == nil && !st.IsDir() {
				if size <= 0 {
					size = st.Size()
					out[i].FileSize = size
				}
			}
		}

		if strings.HasPrefix(strings.ToLower(out[i].MediaType), "image/") && size > DefaultMaxImageBytes {
			return nil, fmt.Errorf("image attachment too large: %s (%d bytes > %d)", out[i].FileName, size, DefaultMaxImageBytes)
		}
	}

	return out, nil
}

func detectMediaType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	if mt := strings.TrimSpace(mime.TypeByExtension(ext)); mt != "" {
		if idx := strings.Index(mt, ";"); idx > 0 {
			mt = mt[:idx]
		}
		return mt
	}

	f, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil || n == 0 {
		return ""
	}
	mt := http.DetectContentType(buf[:n])
	if idx := strings.Index(mt, ";"); idx > 0 {
		mt = mt[:idx]
	}
	if mt == "application/octet-stream" {
		return ""
	}
	return mt
}
