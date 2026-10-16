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

func TestDecodeAttachmentTextDoesNotGuessLegacyEncodings(t *testing.T) {
	for _, data := range [][]byte{[]byte("caf\xe9"), {0x93, 0xfa, 0x96, 0x7b}, {0xff, 0x80}, {'h', 0, 'i', 0}, {0xff, 0xfe, 0, 0xd8}, {0xff, 0xfe, 0, 0, 0, 0, 0x11, 0}} {
		if got, ok := DecodeAttachmentText(data); ok {
			t.Fatalf("guessed unsupported encoding: %q", got)
		}
	}
	if got, ok := DecodeAttachmentText([]byte("café 日本語")); !ok || got != "café 日本語" {
		t.Fatalf("UTF-8: %q %v", got, ok)
	}
}
