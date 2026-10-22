// Package filecontent supports file uploads and previews without altering originals.
package filecontent

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const MaxFileBytes int64 = 25 << 20
const MaxVideoBytes int64 = 200 << 20
const MaxTextBytes int64 = 256 << 10

func invalid(message string) error { return fmt.Errorf("%s", message) }

func UploadLimit(name string) int64 {
	if strings.HasPrefix(FileMediaType(name, nil), "video/") {
		return MaxVideoBytes
	}
	return MaxFileBytes
}

func ReadPath(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("file exceeds the %d byte limit", limit)
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds the %d byte limit", limit)
	}
	return data, err
}
