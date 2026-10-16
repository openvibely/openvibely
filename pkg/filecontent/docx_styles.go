package filecontent

import (
	"archive/zip"
	"encoding/xml"
	"io"
)

// Only style identity is needed for the reference converter's default style map.
// Do not turn arbitrary Word style definitions into CSS or trust embedded HTML.
func docxStyleNames(z *zip.Reader) (map[string]string, error) {
	names := map[string]string{}
	seen := false
	for _, file := range z.File {
		if file.Name != "word/styles.xml" {
			continue
		}
		if seen || file.UncompressedSize64 > 1<<20 {
			return nil, invalid("Office styles exceed preview limits")
		}
		seen = true
		r, err := file.Open()
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer r.Close()
			d := xml.NewDecoder(io.LimitReader(r, (1<<20)+1))
			depth, count := 0, 0
			kind, id, name := "", "", ""
			styleDepth := 0
			for {
				token, err := d.Token()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return invalid("Could not read Office styles")
				}
				switch token := token.(type) {
				case xml.StartElement:
					depth++
					if depth > 64 || d.InputOffset() > 1<<20 {
						return invalid("Office styles exceed preview limits")
					}
					if token.Name.Local == "style" {
						count++
						if count > 4096 || styleDepth != 0 {
							return invalid("Office styles exceed preview limits")
						}
						styleDepth = depth
						kind = docxAttribute(token.Attr, "type")
						id = docxAttribute(token.Attr, "styleId")
						name = ""
						if len(id) > 200 {
							return invalid("Office style identity exceeds preview limits")
						}
					}
					if token.Name.Local == "name" && styleDepth > 0 && depth == styleDepth+1 {
						name = docxAttribute(token.Attr, "val")
						if len(name) > 200 {
							return invalid("Office style name exceeds preview limits")
						}
					}
				case xml.EndElement:
					if depth == styleDepth {
						if id != "" && (kind == "paragraph" || kind == "character") {
							key := kind + ":" + id
							if _, exists := names[key]; !exists {
								names[key] = name
							}
						}
						styleDepth = 0
					}
					depth--
				}
			}
		}()
		if err != nil {
			return nil, err
		}
	}
	return names, nil
}
