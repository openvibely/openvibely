package filecontent

import "testing"

func TestDecodeAttachmentEncodings(t *testing.T) {
	for _, data := range [][]byte{{0xff, 0xfe, 'h', 0, 'i', 0}, {0xfe, 0xff, 0, 'h', 0, 'i'}, {0xff, 0xfe, 0, 0, 'h', 0, 0, 0, 'i', 0, 0, 0}, {0, 0, 0xfe, 0xff, 0, 0, 0, 'h', 0, 0, 0, 'i'}} {
		if got, ok := DecodeAttachmentText(data); !ok || got != "hi" {
			t.Fatalf("decode %x: %q %v", data, got, ok)
		}
	}
	if _, ok := DecodeAttachmentText([]byte{0, 1, 0, 2, 3}); ok {
		t.Fatal("binary treated as text")
	}
}
