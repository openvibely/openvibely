package components

import (
	"embed"
	"path/filepath"
	"strings"
)

//go:embed fileicons/*.svg
var fileIconAssets embed.FS

// Only trusted, embedded Tabler SVGs can reach the raw-HTML renderer.
var fileIconSVGs = func() map[string]string {
	icons := map[string]string{}
	for kind, name := range map[string]string{
		"word": "file-type-doc", "pdf": "file-type-pdf", "spreadsheet": "file-type-xls",
		"presentation": "file-type-ppt", "image": "photo", "audio": "music",
		"video": "video", "archive": "file-zip", "file": "file",
	} {
		data, err := fileIconAssets.ReadFile("fileicons/" + name + ".svg")
		if err != nil {
			panic(err)
		}
		icons[kind] = string(data)
	}
	return icons
}()

// Prefer extensions so Office files reported as ZIP still get document icons.
// Icon classification does not change which formats the viewer can open.
func attachmentIconKind(name, mediaType string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".doc", ".docx", ".docm", ".dot", ".dotx", ".dotm", ".odt", ".rtf":
		return "word"
	case ".pdf":
		return "pdf"
	case ".xls", ".xlsx", ".xlsm", ".xlsb", ".xlt", ".xltx", ".xltm", ".ods", ".csv", ".tsv":
		return "spreadsheet"
	case ".ppt", ".pptx", ".pptm", ".pot", ".potx", ".potm", ".pps", ".ppsx", ".ppsm", ".odp":
		return "presentation"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".avif", ".bmp", ".tif", ".tiff", ".heic", ".heif", ".ico":
		return "image"
	case ".mp3", ".wav", ".flac", ".aac", ".m4a", ".ogg", ".opus", ".aiff", ".aif":
		return "audio"
	case ".mp4", ".mov", ".webm", ".mkv", ".avi", ".m4v", ".mpeg", ".mpg":
		return "video"
	case ".zip", ".tar", ".gz", ".tgz", ".bz2", ".tbz2", ".xz", ".txz", ".zst", ".7z", ".rar":
		return "archive"
	}
	mediaType = strings.ToLower(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0]))
	switch mediaType {
	case "application/pdf":
		return "pdf"
	case "application/msword", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.oasis.opendocument.text", "application/rtf":
		return "word"
	case "application/vnd.ms-excel", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "application/vnd.oasis.opendocument.spreadsheet", "text/csv", "text/tab-separated-values":
		return "spreadsheet"
	case "application/vnd.ms-powerpoint", "application/vnd.openxmlformats-officedocument.presentationml.presentation", "application/vnd.oasis.opendocument.presentation":
		return "presentation"
	case "application/zip", "application/x-zip-compressed", "application/gzip", "application/x-tar", "application/x-7z-compressed", "application/vnd.rar":
		return "archive"
	}
	for _, kind := range []string{"image", "audio", "video"} {
		if strings.HasPrefix(mediaType, kind+"/") {
			return kind
		}
	}
	return "file"
}
