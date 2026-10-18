package agenttools

import (
	"bytes"
	"io"
	"testing"
)

type countingReaderAt struct {
	data      []byte
	bytesRead int
}

func (r *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	r.bytesRead += n
	if n != len(p) {
		return n, io.EOF
	}
	return n, nil
}

func TestContainsNULInFileSamplesReadsBoundedData(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1<<20)
	data[len(data)-1] = 0
	reader := &countingReaderAt{data: data}
	buffer := make([]byte, binaryTailCheckSize)

	containsNUL, err := containsNULInFileSamples(reader, int64(len(data)), buffer)
	if err != nil {
		t.Fatalf("sample file for NUL: %v", err)
	}
	if !containsNUL {
		t.Fatal("expected trailing NUL to be detected")
	}
	if reader.bytesRead >= len(data) {
		t.Fatalf("NUL probe read %d of %d bytes; expected a bounded sample", reader.bytesRead, len(data))
	}
	if wantMax := binaryCheckSize + binaryTailCheckSize; reader.bytesRead > wantMax {
		t.Fatalf("NUL probe read %d bytes, want at most %d", reader.bytesRead, wantMax)
	}
}
