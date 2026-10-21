package builtinskills

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Exercise the actual model-facing recipes against a real release executable.
// All artifacts live in the integration test's isolated temporary directory.
func exerciseGeneralDocumentEditExample(t *testing.T, executable, office, work, kind string, skill []byte) {
	t.Helper()
	block := regexp.MustCompile("(?s)### General editing example\\n.*?```sh\\n(.*?)\\n```").FindSubmatch(skill)
	if len(block) != 2 {
		t.Fatal("missing general editing recipe")
	}
	cmd := exec.Command("bash", "-euc", string(block[1]))
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "PAPERWORKS="+executable, "DOCUMENT_SOFFICE="+office)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s general editing recipe: %v\n%s", kind, err, out)
	}
	var previews string
	switch kind {
	case "word":
		p := readExamplePackage(t, filepath.Join(work, "ordinary-edit.docx"))
		body := string(p["word/document.xml"])
		if !strings.Contains(body, "Recorded revenue totaled $200") || !strings.Contains(body, "Subscriptions") || strings.Contains(body, "<w:ins") || strings.Contains(body, "<w:del") {
			t.Fatal("ordinary edit missing, lost table content, or added tracked changes")
		}
		requireExampleXMLAttribute(t, p["word/document.xml"], "spacing", "after", "160")
		previews = "ordinary-preview"
	case "spreadsheets":
		p := readExamplePackage(t, filepath.Join(work, "formula-results", "formula-edit.xlsx"))
		var sheet struct {
			Cells []struct {
				Ref     string `xml:"r,attr"`
				Formula string `xml:"f"`
				Value   string `xml:"v"`
			} `xml:"sheetData>row>c"`
		}
		if err := xml.Unmarshal(p["xl/worksheets/sheet1.xml"], &sheet); err != nil {
			t.Fatal(err)
		}
		expected := map[string]string{"E2": "0.6", "E3": "0.4", "F2": "120", "F3": "80"}
		for _, cell := range sheet.Cells {
			if want, ok := expected[cell.Ref]; ok {
				if cell.Value != want {
					t.Fatalf("%s=%s want %s", cell.Ref, cell.Value, want)
				}
				if strings.HasPrefix(cell.Ref, "E") && cell.Formula != "D"+cell.Ref[1:]+"/$D$4" {
					t.Fatalf("unexpected formula at %s: %s", cell.Ref, cell.Formula)
				}
				if strings.HasPrefix(cell.Ref, "F") && cell.Formula != "" {
					t.Fatal("copy-values retained formula")
				}
				delete(expected, cell.Ref)
			}
		}
		if len(expected) > 0 {
			t.Fatalf("missing saved results: %v", expected)
		}
		imported := readExamplePackage(t, filepath.Join(work, "imported.xlsx"))
		if !strings.Contains(string(imported["xl/sharedStrings.xml"]), "00123") {
			t.Fatal("imported ID lost leading zeros")
		}
		previews = "formula-preview"
	case "presentations":
		p := readExamplePackage(t, filepath.Join(work, "object-edited.pptx"))
		var presentation struct {
			Slides []struct {
				ID string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
			} `xml:"sldIdLst>sldId"`
		}
		if err := xml.Unmarshal(p["ppt/presentation.xml"], &presentation); err != nil || len(presentation.Slides) != 2 {
			t.Fatalf("expected two slides: %+v %v", presentation, err)
		}
		first := exampleRelatedPart(t, p, "ppt/presentation.xml", presentation.Slides[0].ID, "")
		second := exampleRelatedPart(t, p, "ppt/presentation.xml", presentation.Slides[1].ID, "")
		if !strings.Contains(string(p[first]), "Subscriptions generated 60%") || !strings.Contains(string(p[second]), "Revenue detail") {
			t.Fatal("wrong original or duplicate slide text")
		}
		notes := exampleRelatedPart(t, p, second, "", "/notesSlide")
		if !strings.Contains(string(p[notes]), "Duplicate for detailed discussion.") {
			t.Fatal("notes missing")
		}
		previews = "object-previews"
	case "pdf":
		data, err := os.ReadFile(filepath.Join(work, "annotated-text.txt"))
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"Revenue totaled $200", "Café", "Δοκιμή"} {
			if !strings.Contains(string(data), text) {
				t.Fatalf("PDF text lost: %s", text)
			}
		}
		layout, err := os.ReadFile(filepath.Join(work, "annotated-layout.json"))
		if err != nil {
			t.Fatal(err)
		}
		var pages []struct {
			Characters []struct {
				Text string `json:"text"`
			} `json:"characters"`
		}
		if err := json.Unmarshal(layout, &pages); err != nil || len(pages) != 1 || len(pages[0].Characters) == 0 {
			t.Fatalf("missing positioned text: %s (%v)", layout, err)
		}
		previews = "annotated-preview"
	}
	entries, err := os.ReadDir(filepath.Join(work, previews))
	if err != nil || len(entries) == 0 {
		t.Fatalf("missing previews: %v", err)
	}
	if kind == "pdf" || kind == "presentations" {
		if _, err := os.Stat(filepath.Join(work, previews, "contact-sheet.png")); err != nil {
			t.Fatal(err)
		}
	}
}

// Resolve parts through OPC relationships; slide order does not determine filenames.
func exampleRelatedPart(t *testing.T, parts map[string][]byte, source, id, typeSuffix string) string {
	t.Helper()
	relPath := path.Join(path.Dir(source), "_rels", path.Base(source)+".rels")
	var rels struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Type   string `xml:"Type,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := xml.Unmarshal(parts[relPath], &rels); err != nil {
		t.Fatal(err)
	}
	for _, rel := range rels.Items {
		if (id != "" && rel.ID == id) || (typeSuffix != "" && strings.HasSuffix(rel.Type, typeSuffix)) {
			target := path.Join(path.Dir(source), rel.Target)
			if strings.HasPrefix(rel.Target, "/") {
				target = strings.TrimPrefix(rel.Target, "/")
			}
			if _, ok := parts[target]; !ok {
				t.Fatalf("missing related part %s", target)
			}
			return target
		}
	}
	t.Fatalf("missing relationship from %s: id=%s type=%s", source, id, typeSuffix)
	return ""
}
