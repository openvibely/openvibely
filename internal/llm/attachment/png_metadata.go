package attachment

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"iter"
)

func preservePNGMetadata(original, encoded []byte, keepEXIFAndRGBProfiles bool) []byte {
	var metadata []byte
	for chunk := range pngChunks(original) {
		switch string(chunk[4:8]) {
		case "iCCP":
			if !keepEXIFAndRGBProfiles || pngProfileIsRGB(chunk[8:len(chunk)-4]) {
				metadata = append(metadata, chunk...)
			}
		case "eXIf":
			if keepEXIFAndRGBProfiles {
				metadata = append(metadata, chunk...)
			}
		}
	}
	if len(metadata) == 0 {
		return encoded
	}
	const afterIHDR = 8 + 12 + 13
	result := make([]byte, 0, len(encoded)+len(metadata))
	result = append(result, encoded[:afterIHDR]...)
	result = append(result, metadata...)
	return append(result, encoded[afterIHDR:]...)
}

func pngChunks(original []byte) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		for chunks := original[8:]; len(chunks) >= 12 && string(chunks[4:8]) != "IEND"; {
			end := 12 + uint64(binary.BigEndian.Uint32(chunks))
			if end > uint64(len(chunks)) {
				break
			}
			if !yield(chunks[:end]) {
				return
			}
			chunks = chunks[end:]
		}
	}
}

func pngProfileReader(chunk []byte) (io.ReadCloser, error) {
	nameEnd := bytes.IndexByte(chunk, 0)
	if nameEnd < 1 || nameEnd > 79 || nameEnd+2 >= len(chunk) || chunk[nameEnd+1] != 0 {
		return nil, io.ErrUnexpectedEOF
	}
	return zlib.NewReader(bytes.NewReader(chunk[nameEnd+2:]))
}

func pngProfileIsRGB(chunk []byte) bool {
	r, err := pngProfileReader(chunk)
	if err != nil {
		return false
	}
	defer r.Close()
	var header [20]byte
	_, err = io.ReadFull(r, header[:])
	return err == nil && string(header[16:]) == "RGB "
}
