package components

import "testing"

func TestAttachmentIconClassification(t *testing.T) {
	for _, tc := range []struct{ name, media, want string }{
		{"report.DOCX", "application/zip", "word"},
		{"report.PDF", "application/octet-stream", "pdf"},
		{"book.xlsm", "", "spreadsheet"},
		{"slides.PPTX", "application/zip", "presentation"},
		{"bundle.tar.gz", "", "archive"},
		{"photo.webp", "", "image"},
		{"clip.m4a", "", "audio"},
		{"clip.webm", "", "video"},
		{"download", "application/pdf; charset=binary", "pdf"},
		{"download", "image/png", "image"},
		{"notes.txt", "text/plain", "file"},
		{"unknown.bin", "application/octet-stream", "file"},
		{"<script>.pdf", "", "pdf"},
	} {
		if got := attachmentIconKind(tc.name, tc.media); got != tc.want {
			t.Errorf("%q: got %s, want %s", tc.name, got, tc.want)
		}
	}
}
