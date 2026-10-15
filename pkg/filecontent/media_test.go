package filecontent

import (
	"testing"
)

func TestReferenceMediaCatalogue(t *testing.T) {
	for _, tc := range []struct{ ext, media string }{
		{"avif", "image/avif"}, {"bmp", "image/bmp"}, {"ico", "image/x-icon"}, {"heic", "image/heic"}, {"heif", "image/heif"}, {"m4v", "video/mp4"}, {"mov", "video/quicktime"}, {"ogv", "video/ogg"}, {"webm", "video/webm"}, {"aac", "audio/aac"}, {"flac", "audio/flac"}, {"m4a", "audio/mp4"}, {"oga", "audio/ogg"}, {"opus", "audio/ogg"}, {"weba", "audio/webm"},
	} {
		if got := FileMediaType("sample."+tc.ext, []byte{0, 1, 2}); got != tc.media {
			t.Errorf("%s: %s", tc.ext, got)
		}

	}
	if got := FileMediaType("unsafe.svg", []byte("<html>bad</html>")); got != "text/plain" {
		t.Fatal(got)
	}
}
