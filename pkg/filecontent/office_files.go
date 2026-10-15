package filecontent

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

type DocumentPreview struct {
	Blocks []DocumentBlock
	Notes  []DocumentNote
}

type DocumentTable struct {
	Rows       [][]DocumentTableCell
	HeaderRows int
}

type DocumentTableCell struct {
	Blocks  []DocumentBlock
	Runs    []DocumentRun
	Header  bool
	ColSpan int
	RowSpan int
	Omit    bool
	merge   string
}

type DocumentBlock struct {
	Kind        string
	List        bool
	ListOrdered bool
	ListLevel   int
	ListMarker  string
	Alignment   string
	IndentStart int
	IndentEnd   int
	FirstLine   int
	Hanging     int
	Runs        []DocumentRun
	Table       *DocumentTable
}

type DocumentRun struct {
	BookmarkID                                   string
	NoteKey                                      string
	NoteNumber                                   int
	NoteReference                                string
	Text                                         string
	Link                                         string
	ImageDataURI                                 string
	ImageAlt                                     string
	Bold, Italic, Underline, Strike, Superscript bool
	Subscript, Break                             bool
}

type docxListLevel struct {
	Format string
	Start  int
	Text   string
}

type docxNumberingValue struct {
	Val string `xml:"val,attr"`
}

type docxNumberingLevelXML struct {
	ILvl    string             `xml:"ilvl,attr"`
	Start   docxNumberingValue `xml:"start"`
	NumFmt  docxNumberingValue `xml:"numFmt"`
	LvlText docxNumberingValue `xml:"lvlText"`
}

type docxAbstractNumberingXML struct {
	ID     string                  `xml:"abstractNumId,attr"`
	Levels []docxNumberingLevelXML `xml:"lvl"`
}

type docxLevelOverrideXML struct {
	ILvl          string                 `xml:"ilvl,attr"`
	StartOverride docxNumberingValue     `xml:"startOverride"`
	Level         *docxNumberingLevelXML `xml:"lvl"`
}

type docxNumberingXML struct {
	Abstract []docxAbstractNumberingXML `xml:"abstractNum"`
	Nums     []struct {
		ID         string                 `xml:"numId,attr"`
		AbstractID docxNumberingValue     `xml:"abstractNumId"`
		Overrides  []docxLevelOverrideXML `xml:"lvlOverride"`
	} `xml:"num"`
}

func (b DocumentBlock) Text() string {
	var out strings.Builder
	for _, run := range b.Runs {
		out.WriteString(run.Text)
		if run.Break {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func docxHyperlinkTargets(z *zip.Reader, parts ...string) (map[string]string, error) {
	part := "word/_rels/document.xml.rels"
	if len(parts) > 0 {
		part = parts[0]
	}
	links := map[string]string{}
	for _, file := range z.File {
		if file.Name != part {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		dec := xml.NewDecoder(io.LimitReader(rc, 1<<20))
		for {
			token, err := dec.Token()
			if err == io.EOF {
				return links, nil
			}
			if err != nil {
				return nil, invalid("Could not read Office relationships")
			}
			start, ok := token.(xml.StartElement)
			if !ok || start.Name.Local != "Relationship" {
				continue
			}
			kind := docxAttribute(start.Attr, "Type")
			if !strings.HasSuffix(kind, "/hyperlink") {
				continue
			}
			id := docxAttribute(start.Attr, "Id")
			if id == "" {
				continue
			}
			if link := docxSafeHyperlink(docxAttribute(start.Attr, "Target")); link != "" {
				links[id] = link
			}
		}
	}
	return links, nil
}

func docxSafeHyperlink(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Host == "" {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

type docxImageAsset struct {
	DataURI string
}

func docxImagePartPath(raw string) string {
	target := strings.TrimSpace(raw)
	if target == "" || strings.HasPrefix(target, "/") {
		return ""
	}
	clean := path.Clean(target)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	resolved := path.Join("word", clean)
	if !strings.HasPrefix(resolved, "word/media/") {
		return ""
	}
	return resolved
}

func docxImageMediaType(name string, data []byte) string {
	detected := http.DetectContentType(data)
	switch detected {
	case "image/png", "image/jpeg", "image/gif":
		return detected
	}
	if strings.EqualFold(filepath.Ext(name), ".webp") && len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp"
	}
	if strings.EqualFold(filepath.Ext(name), ".svg") {
		decoder := xml.NewDecoder(bytes.NewReader(data))
		root := false
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				if root {
					return "image/svg+xml"
				}
				return ""
			}
			if err != nil {
				return ""
			}
			switch value := token.(type) {
			case xml.Directive:
				return ""
			case xml.StartElement:
				if !root {
					if value.Name.Local != "svg" || value.Name.Space != "http://www.w3.org/2000/svg" {
						return ""
					}
					root = true
				}
			}
		}
	}
	return ""
}

func docxEmbeddedImages(z *zip.Reader) (map[string]docxImageAsset, error) {
	targets := map[string]string{}
	for _, file := range z.File {
		if file.Name != "word/_rels/document.xml.rels" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		dec := xml.NewDecoder(io.LimitReader(rc, 1<<20))
		for {
			token, tokenErr := dec.Token()
			if tokenErr == io.EOF {
				break
			}
			if tokenErr != nil {
				rc.Close()
				return nil, invalid("Could not read Office image relationships")
			}
			start, ok := token.(xml.StartElement)
			if !ok || start.Name.Local != "Relationship" || strings.EqualFold(docxAttribute(start.Attr, "TargetMode"), "External") {
				continue
			}
			if !strings.HasSuffix(docxAttribute(start.Attr, "Type"), "/image") {
				continue
			}
			id := docxAttribute(start.Attr, "Id")
			part := docxImagePartPath(docxAttribute(start.Attr, "Target"))
			if id != "" && part != "" {
				targets[id] = part
			}
		}
		rc.Close()
	}
	images := make(map[string]docxImageAsset, len(targets))
	var total uint64
	for id, target := range targets {
		var part *zip.File
		for _, file := range z.File {
			if file.Name == target {
				if part != nil {
					return nil, invalid("Office image part is ambiguous")
				}
				part = file
			}
		}
		if part == nil {
			continue
		}
		if part.UncompressedSize64 > 4<<20 || total+part.UncompressedSize64 > 8<<20 {
			return nil, invalid("Office images exceed preview limit")
		}
		rc, err := part.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, (4<<20)+1))
		rc.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(data) > 4<<20 {
			return nil, invalid("Office image exceeds preview limit")
		}
		total += uint64(len(data))
		mediaType := docxImageMediaType(target, data)
		if mediaType == "" {
			continue
		}
		images[id] = docxImageAsset{DataURI: "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)}
	}
	return images, nil
}

func docxNumberingDefinitions(z *zip.Reader) (map[string]map[int]docxListLevel, error) {
	var data []byte
	for _, file := range z.File {
		if file.Name != "word/numbering.xml" {
			continue
		}
		if data != nil || file.UncompressedSize64 > 1<<20 {
			return nil, invalid("Office numbering exceeds preview limit")
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, err = io.ReadAll(io.LimitReader(rc, 1<<20))
		rc.Close()
		if err != nil {
			return nil, err
		}
	}
	if data == nil {
		return map[string]map[int]docxListLevel{}, nil
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, invalid("Could not read Office numbering XML")
		}
		if _, ok := token.(xml.Directive); ok {
			return nil, invalid("Could not read Office numbering XML")
		}
	}
	var document docxNumberingXML
	if err := xml.Unmarshal(data, &document); err != nil {
		return nil, invalid("Could not read Office numbering XML")
	}
	abstracts := make(map[string]map[int]docxListLevel, len(document.Abstract))
	for _, item := range document.Abstract {
		levels := map[int]docxListLevel{}
		for _, level := range item.Levels {
			ilvl, err := strconv.Atoi(level.ILvl)
			if err != nil || ilvl < 0 || ilvl > 8 {
				continue
			}
			start := 1
			if parsed, err := strconv.Atoi(level.Start.Val); err == nil && parsed > 0 && parsed <= 1000000 {
				start = parsed
			}
			format := strings.ToLower(strings.TrimSpace(level.NumFmt.Val))
			if format == "" {
				format = "bullet"
			}
			text := strings.TrimSpace(level.LvlText.Val)
			if len(text) > 128 {
				text = ""
			}
			levels[ilvl] = docxListLevel{Format: format, Start: start, Text: text}
		}
		abstracts[item.ID] = levels
	}
	definitions := make(map[string]map[int]docxListLevel, len(document.Nums))
	for _, item := range document.Nums {
		base := abstracts[item.AbstractID.Val]
		if base == nil {
			continue
		}
		levels := make(map[int]docxListLevel, len(base))
		for ilvl, level := range base {
			levels[ilvl] = level
		}
		for _, override := range item.Overrides {
			ilvl, err := strconv.Atoi(override.ILvl)
			if err != nil || ilvl < 0 || ilvl > 8 {
				continue
			}
			level := levels[ilvl]
			if override.Level != nil {
				if format := strings.TrimSpace(override.Level.NumFmt.Val); format != "" {
					level.Format = strings.ToLower(format)
				}
				if text := strings.TrimSpace(override.Level.LvlText.Val); text != "" && len(text) <= 128 {
					level.Text = text
				}
				if start, err := strconv.Atoi(override.Level.Start.Val); err == nil && start > 0 && start <= 1000000 {
					level.Start = start
				}
			}
			if start, err := strconv.Atoi(override.StartOverride.Val); err == nil && start > 0 && start <= 1000000 {
				level.Start = start
			}
			levels[ilvl] = level
		}
		definitions[item.ID] = levels
	}
	return definitions, nil
}

func docxListNumber(format string, number int) string {
	if number < 1 {
		number = 1
	}
	switch format {
	case "lowerletter":
		var out []byte
		for number > 0 {
			number--
			out = append([]byte{byte('a' + number%26)}, out...)
			number /= 26
		}
		return string(out)
	case "upperletter":
		return strings.ToUpper(docxListNumber("lowerletter", number))
	case "lowerroman", "upperroman":
		values := []struct {
			value  int
			symbol string
		}{{1000, "m"}, {900, "cm"}, {500, "d"}, {400, "cd"}, {100, "c"}, {90, "xc"}, {50, "l"}, {40, "xl"}, {10, "x"}, {9, "ix"}, {5, "v"}, {4, "iv"}, {1, "i"}}
		var out strings.Builder
		for _, item := range values {
			for number >= item.value {
				out.WriteString(item.symbol)
				number -= item.value
			}
		}
		result := out.String()
		if format == "upperroman" {
			return strings.ToUpper(result)
		}
		return result
	default:
		return strconv.Itoa(number)
	}
}

func docxListMarker(level docxListLevel, number, ilvl int) (string, bool) {
	format := strings.ToLower(level.Format)
	if format == "bullet" || format == "none" {
		if level.Text != "" {
			return level.Text, false
		}
		return "•", false
	}
	value := docxListNumber(format, number)
	marker := level.Text
	if marker == "" {
		marker = "%" + strconv.Itoa(ilvl+1) + "."
	}
	placeholder := "%" + strconv.Itoa(ilvl+1)
	if strings.Contains(marker, placeholder) {
		marker = strings.ReplaceAll(marker, placeholder, value)
	} else {
		for i := 1; i <= 9; i++ {
			marker = strings.ReplaceAll(marker, "%"+strconv.Itoa(i), value)
		}
	}
	return marker, true
}

func normalizeDocxTable(table *DocumentTable) {
	if table == nil {
		return
	}
	table.HeaderRows = 0
	for _, row := range table.Rows {
		if len(row) == 0 {
			break
		}
		allHeaders := true
		for i := range row {
			if row[i].ColSpan < 1 || row[i].ColSpan > 20 {
				row[i].ColSpan = 1
			}
			if row[i].RowSpan < 1 || row[i].RowSpan > 1000 {
				row[i].RowSpan = 1
			}
			if !row[i].Header {
				allHeaders = false
			}
		}
		if !allHeaders {
			break
		}
		table.HeaderRows++
	}
	// Merge positions are grid columns, not cell indexes: earlier cells can span
	// different numbers of columns on adjacent rows.
	previous := map[int]*DocumentTableCell{}
	for rowIndex := range table.Rows {
		current := map[int]*DocumentTableCell{}
		column := 0
		for cellIndex := range table.Rows[rowIndex] {
			cell := &table.Rows[rowIndex][cellIndex]
			if cell.ColSpan < 1 || cell.ColSpan > 20 {
				cell.ColSpan = 1
			}
			if cell.merge == "continue" && previous[column] != nil {
				owner := previous[column]
				owner.RowSpan++
				cell.Omit = true
				current[column] = owner
			} else {
				if cell.RowSpan < 1 {
					cell.RowSpan = 1
				}
				current[column] = cell
			}
			column += cell.ColSpan
		}
		previous = current
	}
}

// DocxPreview reads Word's main document part into safe display data. It renders
// bounded embedded raster images as data URIs, ignores macros and external image
// targets, and never fetches relationships or remote links.
func DocxPreview(name string, data []byte) (DocumentPreview, bool, error) {
	if !strings.EqualFold(filepath.Ext(name), ".docx") {
		return DocumentPreview{}, false, nil
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return DocumentPreview{}, true, invalid("Invalid Office document")
	}
	if len(z.File) > 5000 {
		return DocumentPreview{}, true, invalid("Office document has too many entries")
	}
	links, err := docxHyperlinkTargets(z)
	if err != nil {
		return DocumentPreview{}, true, err
	}
	numbering, err := docxNumberingDefinitions(z)
	if err != nil {
		return DocumentPreview{}, true, err
	}
	styles, err := docxStyleNames(z)
	if err != nil {
		return DocumentPreview{}, true, err
	}
	images, err := docxEmbeddedImages(z)
	if err != nil {
		return DocumentPreview{}, true, err
	}
	var part *zip.File
	for _, file := range z.File {
		if file.Name != "word/document.xml" {
			continue
		}
		if part != nil || file.UncompressedSize64 > 16<<20 {
			return DocumentPreview{}, true, invalid("Office text exceeds 16 MB")
		}
		part = file
	}
	if part == nil {
		return DocumentPreview{}, true, invalid("Office document has no readable content")
	}
	rc, err := part.Open()
	if err != nil {
		return DocumentPreview{}, true, err
	}
	defer rc.Close()
	preview, supported, err := parseDocxPreview(io.LimitReader(rc, 16<<20), links, numbering, images, styles)
	if err != nil {
		return preview, supported, err
	}
	notes, err := docxNotes(z, numbering, styles)
	if err != nil {
		return DocumentPreview{}, true, err
	}
	if err := linkDocxNotes(&preview, notes); err != nil {
		return DocumentPreview{}, true, err
	}
	return preview, true, nil
}

func parseDocxPreview(reader io.Reader, links map[string]string, numbering map[string]map[int]docxListLevel, images map[string]docxImageAsset, styleMaps ...map[string]string) (DocumentPreview, bool, error) {
	styles := map[string]string{}
	if len(styleMaps) > 0 {
		styles = styleMaps[0]
	}
	bookmarks := map[string]bool{}
	var pendingBookmarks []DocumentRun
	namedStrong := false
	dec := xml.NewDecoder(reader)
	preview := DocumentPreview{}
	var block *DocumentBlock
	var table *DocumentTable
	var cell *DocumentTableCell
	type tableContext struct {
		table  *DocumentTable
		cell   *DocumentTableCell
		header bool
	}
	var tables []tableContext
	tableRowHeader := false
	var style DocumentRun
	var hyperlink string
	var hyperlinkStack []string
	var imageRel string
	var imageAlt string
	var inText bool
	var text strings.Builder
	textBytes := 0
	tableCells := 0
	listID := ""
	listLevel := 0
	listCounters := map[string]int{}
	lastListID := ""
	lastList := false
	appendBlock := func() error {
		if block == nil {
			return nil
		}
		if textBytes > 1<<20 {
			return invalid("Office text exceeds preview limit")
		}
		if block.List {
			block.ListLevel = listLevel
			definition, found := numbering[listID][listLevel]
			if !found {
				definition = docxListLevel{Format: "bullet", Start: 1, Text: "•"}
			}
			key := listID + ":" + strconv.Itoa(listLevel)
			current, exists := listCounters[key]
			if !lastList || listID != lastListID {
				current = definition.Start
			} else if !exists {
				current = definition.Start
			} else {
				current++
			}
			listCounters[key] = current
			block.ListMarker, block.ListOrdered = docxListMarker(definition, current, listLevel)
			lastListID, lastList = listID, true
		} else {
			lastList = false
		}
		if table != nil && cell != nil {
			if len(cell.Blocks) > 0 {
				cell.Blocks = append(cell.Blocks, *block)
				block = nil
				return nil
			}
			if len(cell.Runs) > 0 && len(block.Runs) > 0 {
				cell.Runs = append(cell.Runs, DocumentRun{Break: true})
			}
			cell.Runs = append(cell.Runs, block.Runs...)
		} else {
			if len(preview.Blocks) >= 200 {
				return invalid("Office text exceeds preview limit")
			}
			preview.Blocks = append(preview.Blocks, *block)
		}
		block = nil
		return nil
	}
	appendTable := func() error {
		if table == nil {
			return nil
		}
		normalizeDocxTable(table)
		finished := DocumentBlock{Kind: "table", Table: table}
		if len(tables) > 0 {
			parent := tables[len(tables)-1]
			tables = tables[:len(tables)-1]
			table, cell, tableRowHeader = parent.table, parent.cell, parent.header
			if cell == nil {
				return invalid("Nested Office table has no parent cell")
			}
			if len(cell.Runs) > 0 {
				cell.Blocks = append(cell.Blocks, DocumentBlock{Kind: "paragraph", Runs: cell.Runs})
				cell.Runs = nil
			}
			cell.Blocks = append(cell.Blocks, finished)
		} else {
			if len(preview.Blocks) >= 200 {
				return invalid("Office text exceeds preview limit")
			}
			preview.Blocks = append(preview.Blocks, finished)
			table, cell = nil, nil
		}
		return nil
	}
	for {
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return DocumentPreview{}, true, invalid("Could not read Office document XML")
		}
		switch token := token.(type) {
		case xml.Directive:
			return DocumentPreview{}, true, invalid("Could not read Office document XML")
		case xml.StartElement:
			switch token.Name.Local {
			case "tbl":
				if err := appendBlock(); err != nil {
					return DocumentPreview{}, true, err
				}
				if table != nil {
					if len(tables) >= 32 || cell == nil {
						return DocumentPreview{}, true, invalid("Office table nesting exceeds preview limit")
					}
					tables = append(tables, tableContext{table, cell, tableRowHeader})
				}
				table = &DocumentTable{}
				cell = nil
			case "tr":
				if table != nil {
					if len(table.Rows) >= 1000 {
						return DocumentPreview{}, true, invalid("Office table exceeds preview limit")
					}
					table.Rows = append(table.Rows, []DocumentTableCell{})
					tableRowHeader = false
				}
			case "tblHeader":
				if table != nil && len(table.Rows) > 0 {
					tableRowHeader = docxEnabled(token.Attr)
					for i := range table.Rows[len(table.Rows)-1] {
						table.Rows[len(table.Rows)-1][i].Header = tableRowHeader
					}
				}
			case "tc":
				if table != nil {
					if len(table.Rows) == 0 || tableCells >= 5000 {
						return DocumentPreview{}, true, invalid("Office table exceeds preview limit")
					}
					table.Rows[len(table.Rows)-1] = append(table.Rows[len(table.Rows)-1], DocumentTableCell{Header: tableRowHeader, ColSpan: 1, RowSpan: 1})
					cell = &table.Rows[len(table.Rows)-1][len(table.Rows[len(table.Rows)-1])-1]
					tableCells++
				}
			case "gridSpan":
				if cell != nil {
					cell.ColSpan = docxTableSpan(token.Attr)
				}
			case "vMerge":
				if cell != nil {
					switch strings.ToLower(strings.TrimSpace(docxAttribute(token.Attr, "val"))) {
					case "restart":
						cell.merge = "restart"
					case "", "continue":
						cell.merge = "continue"
					}
				}
			case "p":
				if err := appendBlock(); err != nil {
					return DocumentPreview{}, true, err
				}
				block = &DocumentBlock{Kind: "paragraph", Runs: pendingBookmarks}
				pendingBookmarks = nil
				listID = ""
				listLevel = 0
			case "pStyle":
				if block != nil {
					id := docxAttribute(token.Attr, "val")
					block.Kind = docxBlockKind(id)
					if block.Kind == "paragraph" {
						block.Kind = docxBlockKind(styles["paragraph:"+id])
					}
				}
			case "jc":
				if block != nil {
					block.Alignment = docxParagraphAlignment(token.Attr)
				}
			case "ind":
				if block != nil {
					block.IndentStart = docxTwips(token.Attr, "start")
					if block.IndentStart == 0 {
						block.IndentStart = docxTwips(token.Attr, "left")
					}
					block.IndentEnd = docxTwips(token.Attr, "end")
					if block.IndentEnd == 0 {
						block.IndentEnd = docxTwips(token.Attr, "right")
					}
					block.FirstLine = docxTwips(token.Attr, "firstLine")
					block.Hanging = docxTwips(token.Attr, "hanging")
				}
			case "numPr":
				if block != nil {
					block.List = true
				}
			case "numId":
				if block != nil {
					block.List = true
					listID = docxAttribute(token.Attr, "val")
				}
			case "ilvl":
				if block != nil {
					if level, err := strconv.Atoi(docxAttribute(token.Attr, "val")); err == nil && level >= 0 && level <= 8 {
						listLevel = level
					}
				}
			case "bookmarkStart":
				name := docxAttribute(token.Attr, "name")
				if anchor := docxBookmarkID(name); anchor != "" && !bookmarks[anchor] {
					if len(bookmarks) >= 1000 {
						return DocumentPreview{}, true, invalid("Office bookmarks exceed preview limit")
					}
					bookmarks[anchor] = true
					run := DocumentRun{BookmarkID: anchor}
					if block != nil {
						block.Runs = append(block.Runs, run)
					} else {
						pendingBookmarks = append(pendingBookmarks, run)
					}
				}
			case "hyperlink":
				hyperlinkStack = append(hyperlinkStack, hyperlink)
				hyperlink = links[docxAttribute(token.Attr, "id")]
				if name := docxAttribute(token.Attr, "anchor"); name != "" {
					if hyperlink != "" {
						u, err := url.Parse(hyperlink)
						if err == nil && len(name) <= 512 {
							u.Fragment = name
							hyperlink = u.String()
						}
					} else if anchor := docxBookmarkID(name); anchor != "" {
						hyperlink = "#" + anchor
					}
				}
			case "drawing":
				imageRel = ""
				imageAlt = ""
			case "docPr":
				if imageAlt == "" {
					imageAlt = strings.TrimSpace(docxAttribute(token.Attr, "descr"))
					if imageAlt == "" {
						imageAlt = strings.TrimSpace(docxAttribute(token.Attr, "title"))
					}
					if len([]rune(imageAlt)) > 500 {
						imageAlt = string([]rune(imageAlt)[:500])
					}
				}
			case "blip":
				imageRel = docxAttribute(token.Attr, "embed")
				if imageRel == "" {
					imageRel = docxAttribute(token.Attr, "link")
				}
			case "r":
				style = DocumentRun{}
				namedStrong = false
			case "rStyle":
				namedStrong = styles["character:"+docxAttribute(token.Attr, "val")] == "Strong"
			case "b":
				style.Bold = docxEnabled(token.Attr)
			case "i":
				style.Italic = docxEnabled(token.Attr)
			case "u":
				style.Underline = docxEnabled(token.Attr)
			case "strike":
				style.Strike = docxEnabled(token.Attr)
			case "vertAlign":
				switch strings.ToLower(docxAttribute(token.Attr, "val")) {
				case "superscript":
					style.Superscript = true
					style.Subscript = false
				case "subscript":
					style.Subscript = true
					style.Superscript = false
				}
			case "footnoteReference", "endnoteReference":
				if block != nil {
					id, err := strconv.Atoi(docxAttribute(token.Attr, "id"))
					if err == nil && id >= 0 {
						block.Runs = append(block.Runs, DocumentRun{NoteKey: strings.TrimSuffix(token.Name.Local, "Reference") + ":" + strconv.Itoa(id)})
					}
				}
			case "t":
				inText = block != nil
				text.Reset()
			case "br", "cr":
				if block != nil {
					block.Runs = append(block.Runs, DocumentRun{Break: true, Link: hyperlink})
				}
			}
		case xml.CharData:
			if inText {
				textBytes += len(token)
				if textBytes > 1<<20 {
					return DocumentPreview{}, true, invalid("Office text exceeds preview limit")
				}
				text.Write(token)
			}
		case xml.EndElement:
			switch token.Name.Local {
			case "t":
				if inText && block != nil && text.Len() > 0 {
					run := style
					run.Bold = run.Bold || namedStrong
					run.Text = text.String()
					run.Link = hyperlink
					block.Runs = append(block.Runs, run)
				}
				inText = false
			case "p":
				if err := appendBlock(); err != nil {
					return DocumentPreview{}, true, err
				}
			case "hyperlink":
				if n := len(hyperlinkStack); n > 0 {
					hyperlink = hyperlinkStack[n-1]
					hyperlinkStack = hyperlinkStack[:n-1]
				} else {
					hyperlink = ""
				}
			case "drawing":
				if block != nil {
					if image, ok := images[imageRel]; ok {
						run := style
						run.Bold = run.Bold || namedStrong
						run.ImageDataURI = image.DataURI
						run.ImageAlt = imageAlt
						block.Runs = append(block.Runs, run)
					}
				}
				imageRel = ""
				imageAlt = ""
			case "tc":
				if err := appendBlock(); err != nil {
					return DocumentPreview{}, true, err
				}
				cell = nil
			case "tbl":
				if err := appendBlock(); err != nil {
					return DocumentPreview{}, true, err
				}
				if err := appendTable(); err != nil {
					return DocumentPreview{}, true, err
				}
			}
		}
	}
	if err := appendBlock(); err != nil {
		return DocumentPreview{}, true, err
	}
	if err := appendTable(); err != nil {
		return DocumentPreview{}, true, err
	}
	return preview, true, nil
}

func docxAttribute(attrs []xml.Attr, name string) string {
	for _, attr := range attrs {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func docxEnabled(attrs []xml.Attr) bool {
	switch strings.ToLower(docxAttribute(attrs, "val")) {
	case "0", "false", "off", "none":
		return false
	default:
		return true
	}
}

func docxTableSpan(attrs []xml.Attr) int {
	value, err := strconv.Atoi(strings.TrimSpace(docxAttribute(attrs, "val")))
	if err != nil || value < 1 || value > 20 {
		return 1
	}
	return value
}
func docxTwips(attrs []xml.Attr, name string) int {
	raw := strings.TrimSpace(docxAttribute(attrs, name))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < -20000 || value > 20000 {
		return 0
	}
	return value
}

func docxParagraphAlignment(attrs []xml.Attr) string {
	switch strings.ToLower(strings.TrimSpace(docxAttribute(attrs, "val"))) {
	case "start", "left":
		return "left"
	case "end", "right":
		return "right"
	case "center":
		return "center"
	case "both", "distribute":
		return "justify"
	default:
		return ""
	}
}

func docxBlockKind(style string) string {
	style = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(style), " ", ""))
	if style == "heading" {
		return "heading1"
	}
	if strings.HasPrefix(style, "heading") {
		if level, err := strconv.Atoi(strings.TrimPrefix(style, "heading")); err == nil && level >= 1 && level <= 6 {
			return "heading" + strconv.Itoa(level)
		}
	}
	return "paragraph"
}

// Never expose a document-provided bookmark as a DOM identifier. Stable IDs
// allow forward links without trusting arbitrary names as selectors or URLs.
func docxBookmarkID(name string) string {
	if name == "" || name == "_GoBack" || len(name) > 512 {
		return ""
	}
	return fmt.Sprintf("doc-bookmark-%x", sha256.Sum256([]byte(name)))
}
