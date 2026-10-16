package openaiclient

import "testing"

func TestPDFBytesAreNotInlineModelInput(t *testing.T) {
	if IsSupportedFileType("report.pdf") {
		t.Fatal("PDF must use a file reference")
	}
	att := &FileAttachment{FileName: "report.pdf", MediaType: "application/pdf", Data: []byte("%PDF-1.4")}
	if _, err := att.toInputContent(); err == nil {
		t.Fatal("PDF bytes must not be sent inline")
	}
}
