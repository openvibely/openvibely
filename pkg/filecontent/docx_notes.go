package filecontent

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"strconv"
	"strings"
)

type DocumentNote struct {
	Number    int
	Reference string
	Blocks    []DocumentBlock
}

// Read only package-local notes. Note XML is bounded independently of the main
// document, and goes through the same structured preview parser, never raw HTML.
func docxNotes(z *zip.Reader, numbering map[string]map[int]docxListLevel, styles map[string]string) (map[string][]DocumentBlock, error) {
	result := map[string][]DocumentBlock{}
	for _, kind := range []string{"footnote", "endnote"} {
		part := "word/" + kind + "s.xml"
		seen := false
		for _, file := range z.File {
			if file.Name != part {
				continue
			}
			if seen || file.UncompressedSize64 > 2<<20 {
				return nil, invalid("Office notes exceed preview limit")
			}
			seen = true
			r, err := file.Open()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(io.LimitReader(r, (2<<20)+1))
			r.Close()
			if err != nil {
				return nil, err
			}
			if len(data) > 2<<20 {
				return nil, invalid("Office notes exceed preview limit")
			}
			links, err := docxHyperlinkTargets(z, "word/_rels/"+kind+"s.xml.rels")
			if err != nil {
				return nil, err
			}
			dec := xml.NewDecoder(strings.NewReader(string(data)))
			for {
				token, err := dec.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					return nil, invalid("Could not read Office notes")
				}
				start, ok := token.(xml.StartElement)
				if !ok || start.Name.Local != kind {
					continue
				}
				var content struct {
					Inner string `xml:",innerxml"`
				}
				if err := dec.DecodeElement(&content, &start); err != nil {
					return nil, invalid("Could not read Office notes")
				}
				noteType := docxAttribute(start.Attr, "type")
				if noteType != "" && noteType != "normal" {
					continue
				}
				id, err := strconv.Atoi(docxAttribute(start.Attr, "id"))
				if err != nil || id < 0 {
					continue
				}
				key := kind + ":" + strconv.Itoa(id)
				if _, ok := result[key]; ok {
					return nil, invalid("Duplicate Office note")
				}
				if len(result) >= 1000 {
					return nil, invalid("Office notes exceed preview limit")
				}
				preview, _, err := parseDocxPreview(strings.NewReader("<document>"+content.Inner+"</document>"), links, numbering, nil, styles)
				if err != nil {
					return nil, err
				}
				result[key] = preview.Blocks
			}
		}
	}
	return result, nil
}

func linkDocxNotes(document *DocumentPreview, notes map[string][]DocumentBlock) error {
	references, total := 0, 0
	costs := map[string]int{}
	var cost func([]DocumentBlock) int
	cost = func(values []DocumentBlock) int {
		n := 0
		runCost := func(runs []DocumentRun) {
			for _, r := range runs {
				n += 64 + len(r.Text) + len(r.Link)
			}
		}
		for _, b := range values {
			n += 64
			runCost(b.Runs)
			if b.Table != nil {
				for _, row := range b.Table.Rows {
					for _, c := range row {
						n += 32
						runCost(c.Runs)
						n += cost(c.Blocks)
					}
				}
			}
		}
		return n
	}
	var runs func([]DocumentRun) error
	var blocks func([]DocumentBlock) error
	runs = func(values []DocumentRun) error {
		for i := range values {
			r := &values[i]
			if r.NoteKey == "" {
				continue
			}
			references++
			if references > 1000 {
				return invalid("Office note references exceed preview limit")
			}
			body, ok := notes[r.NoteKey]
			if !ok {
				r.Text = "[Missing note]"
				continue
			}
			size, ok := costs[r.NoteKey]
			if !ok {
				size = cost(body)
				costs[r.NoteKey] = size
			}
			total += size
			if total > 2<<20 {
				return invalid("Office note output exceeds preview limit")
			}
			r.NoteReference = "doc-note-ref-" + strconv.Itoa(references)
			number := len(document.Notes) + 1
			document.Notes = append(document.Notes, DocumentNote{Number: number, Reference: r.NoteReference, Blocks: body})
			r.NoteNumber = number
		}
		return nil
	}
	blocks = func(values []DocumentBlock) error {
		for i := range values {
			b := &values[i]
			if err := runs(b.Runs); err != nil {
				return err
			}
			if b.Table != nil {
				for j := range b.Table.Rows {
					for k := range b.Table.Rows[j] {
						c := &b.Table.Rows[j][k]
						if c.Omit {
							continue
						}
						if err := runs(c.Runs); err != nil {
							return err
						}
						if err := blocks(c.Blocks); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	}
	return blocks(document.Blocks)
}
