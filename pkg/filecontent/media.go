package filecontent

import (
	"net/http"
	"path/filepath"
	"strings"
)

// FileMediaType covers the reference media catalogue independently of the host
// OS MIME registry. SVG is validated separately before it can be served as an image.
func FileMediaType(name string, data []byte) string {
	if strings.EqualFold(filepath.Ext(name), ".svg") {
		if IsSVG(name, data) {
			return "image/svg+xml"
		}
		return "text/plain"
	}
	types := map[string]string{
		".avif": "image/avif", ".bmp": "image/bmp", ".gif": "image/gif", ".ico": "image/x-icon", ".jpeg": "image/jpeg", ".jpg": "image/jpeg", ".png": "image/png", ".webp": "image/webp", ".heic": "image/heic", ".heif": "image/heif",
		".m4v": "video/mp4", ".mov": "video/quicktime", ".mp4": "video/mp4", ".ogv": "video/ogg", ".webm": "video/webm",
		".aac": "audio/aac", ".flac": "audio/flac", ".m4a": "audio/mp4", ".mp3": "audio/mpeg", ".oga": "audio/ogg", ".ogg": "audio/ogg", ".opus": "audio/ogg", ".wav": "audio/wav", ".weba": "audio/webm",
	}
	if media := types[strings.ToLower(filepath.Ext(name))]; media != "" {
		return media
	}
	return http.DetectContentType(data)
}
