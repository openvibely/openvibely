package filecontent

import (
	"bytes"
	"encoding/xml"
	"io"
	"path/filepath"
	"strings"
)

// IsSVG checks a bounded, standalone SVG document. Serve it only as an image
// with a restrictive CSP, never as inline HTML or an object/embed document.
func IsSVG(name string, data []byte) bool {
	if !strings.EqualFold(filepath.Ext(name), ".svg") || len(data) == 0 || len(data) > 1<<20 {
		return false
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth, roots := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return roots == 1 && depth == 0
		}
		if err != nil {
			return false
		}
		switch value := token.(type) {
		case xml.Directive:
			return false
		case xml.ProcInst:
			if value.Target != "xml" || roots != 0 {
				return false
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(value)) != "" {
				return false
			}
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 || value.Name.Local != "svg" || value.Name.Space != "http://www.w3.org/2000/svg" {
					return false
				}
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
}
