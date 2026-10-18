package builtinskills

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Execute the instructions shipped to the model, then inspect the saved artifacts.
// Called by the existing opt-in integration test with real Paperworks/Office binaries.
func exerciseDocumentEditExample(t *testing.T, executable, office, work, kind, format string, skill []byte) {
	t.Helper()
	source := filepath.Join(work, "example."+format)
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile("(?s)##+ Editing example\\n.*?```sh\\n(.*?)\\n```").FindSubmatch(skill)
	if len(match) != 2 {
		t.Fatal("missing executable editing example")
	}
	cmd := exec.Command("bash", "-euc", string(match[1]))
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "PAPERWORKS="+executable, "DOCUMENT_SOFFICE="+office)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s editing example: %v\n%s", kind, err, out)
	}
	unchanged, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(original, unchanged) {
		t.Fatal("editing example changed its source")
	}

	var preview string
	switch kind {
	case "pdf":
		form := regexp.MustCompile("(?s)### Form example\\n.*?```sh\\n(.*?)\\n```").FindSubmatch(skill)
		if len(form) != 2 {
			t.Fatal("missing executable form example")
		}
		formCmd := exec.Command("bash", "-euc", string(form[1]))
		formCmd.Dir, formCmd.Env = work, cmd.Env
		if out, err := formCmd.CombinedOutput(); err != nil {
			t.Fatalf("form example: %v\n%s", err, out)
		}
		data, err := os.ReadFile(filepath.Join(work, "form-after.json"))
		if err != nil {
			t.Fatal(err)
		}
		var report struct {
			Fields      map[string]string `json:"fields"`
			HasFormTree bool              `json:"has_form_tree"`
		}
		if err := json.Unmarshal(data, &report); err != nil {
			t.Fatal(err)
		}
		if !report.HasFormTree || report.Fields["reviewer"] != "Alex Smith" || report.Fields["approved"] != "/Yes" {
			t.Fatalf("incorrect saved form: %s", data)
		}
		preview = "draft-preview"
		text, err := os.ReadFile(filepath.Join(work, "draft-text.txt"))
		if err != nil || !strings.Contains(string(text), "DRAFT") || !strings.Contains(string(text), "Revenue totaled $200") {
			t.Fatalf("watermark/content missing: %s (%v)", text, err)
		}
	case "word":
		preview = "reviewed-preview"
		parts := readExamplePackage(t, filepath.Join(work, "reviewed.docx"))
		body := string(parts["word/document.xml"])
		for _, want := range []string{"<w:ins", "<w:del", "Recorded revenue totaled $200", "Revenue totaled $200"} {
			if !strings.Contains(body, want) {
				t.Fatalf("tracked edit missing %q", want)
			}
		}
		if !strings.Contains(string(parts["word/comments.xml"]), "Confirm the reporting period.") {
			t.Fatal("comment missing from edited copy")
		}
	case "presentations":
		preview = "edited-preview"
		parts := readExamplePackage(t, filepath.Join(work, "edited.pptx"))
		body := parts["ppt/slides/slide1.xml"]
		if !strings.Contains(string(body), "contributed") || !strings.Contains(string(body), "USD 120") {
			t.Fatal("slide text/table edit missing")
		}
		if !strings.Contains(string(parts["ppt/notesSlides/notesSlide1.xml"]), "Subscriptions: USD 120") {
			t.Fatal("notes edit missing")
		}
		before := readExamplePackage(t, source)
		if len(before) != len(parts) {
			t.Fatal("slide edit added or removed package parts")
		}
		for name, data := range before {
			if name != "ppt/slides/slide1.xml" && name != "ppt/notesSlides/notesSlide1.xml" && !bytes.Equal(data, parts[name]) {
				t.Fatalf("slide edit changed unrelated part %s", name)
			}
		}
	case "spreadsheets":
		preview = "changed-preview"
		parts := readExamplePackage(t, filepath.Join(work, "calculated", "changed.xlsx"))
		var sheet struct {
			Rows []struct {
				Cells []struct {
					Ref     string `xml:"r,attr"`
					Formula string `xml:"f"`
					Value   string `xml:"v"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		if err := xml.Unmarshal(parts["xl/worksheets/sheet1.xml"], &sheet); err != nil {
			t.Fatal(err)
		}
		expected := map[string]string{"B2": "4", "D2": "160", "D3": "80", "D4": "240"}
		for _, row := range sheet.Rows {
			for _, cell := range row.Cells {
				if want, ok := expected[cell.Ref]; ok {
					if cell.Value != want {
						t.Fatalf("%s = %q, want %q", cell.Ref, cell.Value, want)
					}
					if strings.HasPrefix(cell.Ref, "D") && cell.Formula == "" {
						t.Fatalf("formula lost at %s", cell.Ref)
					}
					delete(expected, cell.Ref)
				}
			}
		}
		if len(expected) != 0 {
			t.Fatalf("missing cells: %v", expected)
		}
	}
	paths, err := filepath.Glob(filepath.Join(work, preview, "*.png"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no preview images: %v", err)
	}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := png.DecodeConfig(f)
		f.Close()
		if err != nil || cfg.Width == 0 || cfg.Height == 0 {
			t.Fatalf("invalid preview %s: %v", path, err)
		}
	}
}

func readExamplePackage(t *testing.T, path string) map[string][]byte {
	t.Helper()
	z, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	parts := map[string][]byte{}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		parts[f.Name] = b
	}
	return parts
}

// Run the added creation recipes from the skill itself and verify native objects,
// not just successful CLI exit codes or the presence of words in the instructions.
func exerciseDocumentCreationOptions(t *testing.T, executable, office, work, kind string, skill []byte) {
	t.Helper()
	headings := map[string]string{"word": "Styles and table layout example", "spreadsheets": "Charts and named tables example", "presentations": "Editable shapes example"}
	heading, ok := headings[kind]
	if !ok {
		return
	}
	block := regexp.MustCompile("(?s)### " + regexp.QuoteMeta(heading) + "\\n.*?```sh\\n(.*?)\\n```").FindSubmatch(skill)
	if len(block) != 2 {
		t.Fatalf("missing %s", heading)
	}
	cmd := exec.Command("bash", "-euc", string(block[1]))
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "PAPERWORKS="+executable, "DOCUMENT_SOFFICE="+office)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", heading, err, out)
	}
	run := func(args ...string) {
		t.Helper()
		c := exec.Command(executable, args...)
		c.Dir, c.Env = work, cmd.Env
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	writeSpec := func(name string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	readSpec := func(name string) map[string]any {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(work, name))
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	switch kind {
	case "word":
		exerciseWordNotesExample(t, executable, office, work, skill)
		p := readExamplePackage(t, filepath.Join(work, "styled-table.docx"))
		requireExampleXMLAttribute(t, p["word/styles.xml"], "style", "styleId", "ReportLead")
		requireExampleXMLAttribute(t, p["word/styles.xml"], "style", "styleId", "Normal")
		requireExampleXMLAttribute(t, p["word/document.xml"], "pStyle", "val", "ReportLead")
		requireExampleXMLAttribute(t, p["word/document.xml"], "tblLayout", "type", "fixed")
		requireExampleXMLAttribute(t, p["word/document.xml"], "jc", "val", "right")
		requireExampleXMLAttribute(t, p["word/document.xml"], "left", "w", "120")
		spec := readSpec("styled-table.json")
		delete(spec, "styles_overlay_file")
		for field, part := range map[string]string{"styles_file": "word/styles.xml", "theme_file": "word/theme/theme1.xml"} {
			name := field + ".xml"
			if len(p[part]) == 0 {
				t.Fatalf("missing resource %s", part)
			}
			if err := os.WriteFile(filepath.Join(work, name), p[part], 0600); err != nil {
				t.Fatal(err)
			}
			spec[field] = name
		}
		spec["blocks"].([]any)[1].(map[string]any)["autofit"] = true
		writeSpec("resources.json", spec)
		run("create-docx", "--spec", "resources.json", "--output", "resources.docx")
		copied := readExamplePackage(t, filepath.Join(work, "resources.docx"))
		for _, part := range []string{"word/styles.xml", "word/theme/theme1.xml"} {
			if !bytes.Equal(p[part], copied[part]) {
				t.Fatalf("resource not preserved: %s", part)
			}
		}
		requireExampleXMLAttribute(t, copied["word/document.xml"], "tblW", "type", "auto")

		// Start with a genuinely indented table, then execute the width-only recipe.
		writeSpec("initial-indent.json", map[string]any{"operation": "table-geometry", "table": 0, "widths_twips": []int{3600, 2400}, "indent_twips": 360})
		run("word-edit", "styled-table.docx", "--spec", "initial-indent.json", "--output", "indented.docx")
		original, err := os.ReadFile(filepath.Join(work, "indented.docx"))
		if err != nil {
			t.Fatal(err)
		}
		recipe := regexp.MustCompile("(?s)### Preserve indentation when resizing a table\\n.*?```sh\\n(.*?)\\n```").FindSubmatch(skill)
		if len(recipe) != 2 {
			t.Fatal("missing executable table resize recipe")
		}
		resize := exec.Command("bash", "-euc", string(recipe[1]))
		resize.Dir, resize.Env = work, cmd.Env
		if out, err := resize.CombinedOutput(); err != nil {
			t.Fatalf("table resize recipe: %v\n%s", err, out)
		}
		unchanged, err := os.ReadFile(filepath.Join(work, "indented.docx"))
		if err != nil || !bytes.Equal(original, unchanged) {
			t.Fatal("table resize changed its source")
		}
		before := readExamplePackage(t, filepath.Join(work, "indented.docx"))
		after := readExamplePackage(t, filepath.Join(work, "resized-table.docx"))
		requireExampleXMLAttribute(t, before["word/document.xml"], "tblInd", "w", "360")
		requireExampleXMLAttribute(t, after["word/document.xml"], "tblInd", "w", "360")
		requireExampleXMLAttribute(t, after["word/document.xml"], "tblW", "w", "6600")
		var table struct {
			Tables []struct {
				Columns []struct {
					Width int `xml:"w,attr"`
				} `xml:"tblGrid>gridCol"`
				Rows []struct {
					Cells []struct {
						Width struct {
							Value int `xml:"w,attr"`
						} `xml:"tcPr>tcW"`
						Text []string `xml:"p>r>t"`
					} `xml:"tc"`
				} `xml:"tr"`
			} `xml:"body>tbl"`
		}
		if err := xml.Unmarshal(after["word/document.xml"], &table); err != nil {
			t.Fatal(err)
		}
		if len(table.Tables) != 1 || len(table.Tables[0].Columns) != 2 || table.Tables[0].Columns[0].Width != 4200 || table.Tables[0].Columns[1].Width != 2400 {
			t.Fatalf("wrong saved grid: %+v", table)
		}
		expectedRows := [][]string{{"Item", "USD"}, {"Subscriptions", "120"}, {"Services", "80"}}
		if len(table.Tables[0].Rows) != len(expectedRows) {
			t.Fatal("table rows changed")
		}
		for i, row := range table.Tables[0].Rows {
			if len(row.Cells) != 2 {
				t.Fatal("table cells changed")
			}
			for j, cell := range row.Cells {
				if cell.Width.Value != []int{4200, 2400}[j] || strings.Join(cell.Text, "") != expectedRows[i][j] {
					t.Fatalf("wrong resized cell %d,%d: %+v", i, j, cell)
				}
			}
		}
		for name, data := range before {
			if name != "word/document.xml" && !bytes.Equal(data, after[name]) {
				t.Fatalf("resize changed unrelated part %s", name)
			}
		}
		previews, err := filepath.Glob(filepath.Join(work, "resized-table-preview", "*.png"))
		if err != nil || len(previews) == 0 {
			t.Fatalf("no table preview: %v", err)
		}
	case "spreadsheets":
		exerciseSpreadsheetPictureExample(t, executable, office, work, skill)
		exerciseSpreadsheetDropdownThemeExample(t, executable, office, work, skill)
		p := readExamplePackage(t, filepath.Join(work, "chart-table.xlsx"))
		requireExampleXMLAttribute(t, p["xl/tables/table1.xml"], "table", "name", "RevenueData")
		requireExampleXMLAttribute(t, p["xl/tables/table1.xml"], "table", "ref", "A1:B4")
		requireExampleXMLAttribute(t, p["xl/charts/chart1.xml"], "numFmt", "formatCode", "$#,##0")
		requireExampleXMLAttribute(t, p["xl/charts/chart1.xml"], "srgbClr", "val", "17324D")
		for _, typ := range []string{"line", "column", "bar", "scatter", "pie", "doughnut", "area"} {
			spec := readSpec("chart-table.json")
			chart := spec["sheets"].([]any)[0].(map[string]any)["charts"].([]any)[0].(map[string]any)
			chart["type"] = typ
			series := chart["series"].([]any)[0].(map[string]any)
			if typ == "pie" || typ == "doughnut" {
				delete(chart, "x_axis")
				delete(chart, "y_axis")
				delete(series, "Line")
				delete(series, "Marker")
				chart["vary_colors"] = true
			} else if typ == "bar" || typ == "column" || typ == "area" {
				delete(series, "Marker")
			}
			writeSpec(typ+".json", spec)
			run("create-xlsx", "--spec", typ+".json", "--output", typ+".xlsx")
			saved := readExamplePackage(t, filepath.Join(work, typ+".xlsx"))
			element := typ + "Chart"
			if typ == "column" || typ == "bar" {
				element = "barChart"
			}
			requireExampleXMLAttribute(t, saved["xl/charts/chart1.xml"], element, "", "")
			if typ == "bar" || typ == "column" {
				dir := "bar"
				if typ == "column" {
					dir = "col"
				}
				requireExampleXMLAttribute(t, saved["xl/charts/chart1.xml"], "barDir", "val", dir)
			}
		}
	case "presentations":
		p := readExamplePackage(t, filepath.Join(work, "shapes.pptx"))
		body := p["ppt/slides/slide1.xml"]
		for _, shape := range []string{"roundRect", "rightArrow", "ellipse"} {
			requireExampleXMLAttribute(t, body, "prstGeom", "prst", shape)
		}
		requireExampleXMLAttribute(t, body, "srgbClr", "val", "17324D")
		requireExampleXMLAttribute(t, body, "pPr", "algn", "ctr")
		// The corrected routing command must support an actual PowerPoint review thread.
		writeSpec("shape-comment.json", map[string]any{"action": "add", "slide": 1, "author": "Reviewer", "text": "Check layout.", "position_emu": []int{914400, 914400}})
		run("slide-comments", "shapes.pptx", "--requirements", "shape-comment.json", "--output", "shape-review.pptx")
		run("slide-comments", "shape-review.pptx")
	}
}

func requireExampleXMLAttribute(t *testing.T, data []byte, element, attribute, value string) {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		e, ok := tok.(xml.StartElement)
		if !ok || e.Name.Local != element {
			continue
		}
		if attribute == "" {
			return
		}
		for _, a := range e.Attr {
			if a.Name.Local == attribute && a.Value == value {
				return
			}
		}
	}
	t.Fatalf("missing output XML element %s with %s=%q", element, attribute, value)
}

// Verify the model-facing recipe saves the intended image dimensions and anchor.
func exerciseSpreadsheetPictureExample(t *testing.T, executable, office, work string, skill []byte) {
	t.Helper()
	fixture := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			fixture.SetRGBA(x, y, color.RGBA{23, 50, 77, 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, fixture); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "chart.png"), encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile("(?s)### Image sizing and positioning example\\n.*?```sh\\n(.*?)\\n```").FindSubmatch(skill)
	if len(block) != 2 {
		t.Fatal("missing executable picture recipe")
	}
	cmd := exec.Command("bash", "-euc", string(block[1]))
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "PAPERWORKS="+executable, "DOCUMENT_SOFFICE="+office)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("picture recipe: %v\n%s", err, out)
	}
	parts := readExamplePackage(t, filepath.Join(work, "picture.xlsx"))
	var drawing struct {
		Anchors []struct {
			From struct {
				Col    int `xml:"col"`
				Row    int `xml:"row"`
				ColOff int `xml:"colOff"`
				RowOff int `xml:"rowOff"`
			} `xml:"from"`
			Ext struct {
				CX int `xml:"cx,attr"`
				CY int `xml:"cy,attr"`
			} `xml:"ext"`
			Picture struct {
				Properties struct {
					Name        string `xml:"name,attr"`
					Description string `xml:"descr,attr"`
				} `xml:"nvPicPr>cNvPr"`
			} `xml:"pic"`
		} `xml:"oneCellAnchor"`
	}
	if err := xml.Unmarshal(parts["xl/drawings/drawing1.xml"], &drawing); err != nil {
		t.Fatal(err)
	}
	if len(drawing.Anchors) != 1 {
		t.Fatalf("expected one move-with-cell image: %+v", drawing)
	}
	a := drawing.Anchors[0]
	const emuPerPixel = 9525
	if a.From.Col != 1 || a.From.Row != 1 || a.From.ColOff != 8*emuPerPixel || a.From.RowOff != 6*emuPerPixel || a.Ext.CX != 200*emuPerPixel || a.Ext.CY != 100*emuPerPixel {
		t.Fatalf("incorrect image placement/dimensions: %+v", a)
	}
	if a.Picture.Properties.Name != "Revenue chart" || a.Picture.Properties.Description != "Revenue by quarter" {
		t.Fatalf("missing image name/description: %+v", a)
	}
	if !bytes.Equal(parts["xl/media/image1.png"], encoded.Bytes()) {
		t.Fatal("embedded image changed")
	}
	paths, err := filepath.Glob(filepath.Join(work, "picture-preview", "*.png"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("missing picture preview: %v (%v)", paths, err)
	}
	preview, err := os.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Close()
	rendered, err := png.Decode(preview)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for y := rendered.Bounds().Min.Y; y < rendered.Bounds().Max.Y && !found; y++ {
		for x := rendered.Bounds().Min.X; x < rendered.Bounds().Max.X; x++ {
			r, g, b, _ := rendered.At(x, y).RGBA()
			if r>>8 == 23 && g>>8 == 50 && b>>8 == 77 {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("image content missing from rendered preview")
	}
}

func exerciseSpreadsheetDropdownThemeExample(t *testing.T, executable, office, work string, skill []byte) {
	t.Helper()
	source := readExamplePackage(t, filepath.Join(work, "example.xlsx"))["xl/theme/theme1.xml"]
	theme := bytes.Replace(source, []byte(`name="Office Theme"`), []byte(`name="Example Company"`), 1)
	if len(theme) == 0 || bytes.Equal(theme, source) {
		t.Fatal("could not prepare distinct company theme fixture")
	}
	if err := os.WriteFile(filepath.Join(work, "company-theme.xml"), theme, 0600); err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile("(?s)### Dropdowns and workbook themes example\\n.*?```sh\\n(.*?)\\n```").FindSubmatch(skill)
	if len(block) != 2 {
		t.Fatal("missing executable dropdown/theme recipe")
	}
	cmd := exec.Command("bash", "-euc", string(block[1]))
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "PAPERWORKS="+executable, "DOCUMENT_SOFFICE="+office)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dropdown/theme recipe: %v\n%s", err, out)
	}
	parts := readExamplePackage(t, filepath.Join(work, "dropdown-theme.xlsx"))
	if !bytes.Equal(parts["xl/theme/theme1.xml"], theme) {
		t.Fatal("custom theme was not retained")
	}
	var sheet struct {
		Rules []struct {
			Type       string `xml:"type,attr"`
			Range      string `xml:"sqref,attr"`
			Formula    string `xml:"formula1"`
			HideArrow  bool   `xml:"showDropDown,attr"`
			AllowBlank bool   `xml:"allowBlank,attr"`
			ShowError  bool   `xml:"showErrorMessage,attr"`
			ErrorStyle string `xml:"errorStyle,attr"`
			ErrorTitle string `xml:"errorTitle,attr"`
			Error      string `xml:"error,attr"`
		} `xml:"dataValidations>dataValidation"`
	}
	if err := xml.Unmarshal(parts["xl/worksheets/sheet1.xml"], &sheet); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"A2:A20": `"Draft,Review,Approved"`, "B2:B20": "AllowedStatuses"}
	if len(sheet.Rules) != len(expected) {
		t.Fatalf("incorrect rule count: %+v", sheet.Rules)
	}
	for _, r := range sheet.Rules {
		if formula, ok := expected[r.Range]; !ok || r.Formula != formula || r.Type != "list" || r.HideArrow || !r.AllowBlank || !r.ShowError || r.ErrorStyle != "stop" || r.ErrorTitle != "Choose a status" || r.Error != "Use a listed status." {
			t.Fatalf("incorrect saved dropdown: %+v", r)
		}
		delete(expected, r.Range)
	}
	if len(expected) != 0 {
		t.Fatalf("missing dropdowns: %v", expected)
	}
	var workbook struct {
		Names []struct {
			Name string `xml:"name,attr"`
			Ref  string `xml:",chardata"`
		} `xml:"definedNames>definedName"`
	}
	if err := xml.Unmarshal(parts["xl/workbook.xml"], &workbook); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range workbook.Names {
		if n.Name == "AllowedStatuses" && n.Ref == "'Lists'!$A$1:$A$3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dropdown source reference missing: %+v", workbook.Names)
	}
	requireExampleXMLAttribute(t, parts["xl/workbook.xml"], "sheet", "state", "hidden")
	preview, err := os.Open(filepath.Join(work, "dropdown-theme-preview", "page-0001.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Close()
	if _, err := png.DecodeConfig(preview); err != nil {
		t.Fatal(err)
	}
}

func exerciseWordNotesExample(t *testing.T, executable, office, work string, skill []byte) {
	t.Helper()
	block := regexp.MustCompile("(?s)## Footnotes and endnotes example\\n.*?```sh\\n(.*?)\\n```").FindSubmatch(skill)
	if len(block) != 2 {
		t.Fatal("missing executable notes recipe")
	}
	cmd := exec.Command("bash", "-euc", string(block[1]))
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "PAPERWORKS="+executable, "DOCUMENT_SOFFICE="+office)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("notes recipe: %v\n%s", err, out)
	}
	source := readExamplePackage(t, filepath.Join(work, "notes-source.docx"))
	intermediate := readExamplePackage(t, filepath.Join(work, "with-footnote.docx"))
	saved := readExamplePackage(t, filepath.Join(work, "with-notes.docx"))
	body := saved["word/document.xml"]
	if bytes.Contains(body, []byte("[[FN]]")) || bytes.Contains(body, []byte("[[EN]]")) {
		t.Fatal("note marker remained in output")
	}
	if !bytes.Contains(source["word/document.xml"], []byte("[[FN]]")) || !bytes.Contains(source["word/document.xml"], []byte("[[EN]]")) {
		t.Fatal("source markers changed")
	}
	if !bytes.Equal(intermediate["word/footnotes.xml"], saved["word/footnotes.xml"]) {
		t.Fatal("endnote addition changed existing footnote")
	}
	requireExampleXMLAttribute(t, body, "b", "", "")
	for kind, text := range map[string]string{"footnote": "Source: quarterly report.", "endnote": "Methodology: compare matching reporting periods."} {
		requireExampleXMLAttribute(t, body, kind+"Reference", "id", "1")
		part := saved["word/"+kind+"s.xml"]
		var notes struct {
			Entries []struct {
				ID   string   `xml:"id,attr"`
				Type string   `xml:"type,attr"`
				Text []string `xml:"p>r>t"`
			} `xml:",any"`
		}
		if err := xml.Unmarshal(part, &notes); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, n := range notes.Entries {
			if n.ID == "1" && n.Type == "" && strings.TrimSpace(strings.Join(n.Text, "")) == text {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s text for body reference: %+v", kind, notes)
		}
		requireExampleXMLAttribute(t, part, kind, "type", "separator")
		requireExampleXMLAttribute(t, part, kind, "type", "continuationSeparator")
		requireExampleXMLAttribute(t, saved["word/_rels/document.xml.rels"], "Relationship", "Type", "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"+kind+"s")
	}
	for name, data := range source {
		if name != "word/document.xml" && name != "word/_rels/document.xml.rels" && name != "[Content_Types].xml" && !bytes.Equal(data, saved[name]) {
			t.Fatalf("notes changed unrelated part %s", name)
		}
	}
	paths, err := filepath.Glob(filepath.Join(work, "notes-preview", "*.png"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("missing note previews: %v", err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
	}
}
