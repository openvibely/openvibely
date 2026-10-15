package filecontent

import (
	"strings"
	"testing"
)

func TestDocxNotesResolveInReferenceOrder(t *testing.T) {
	data := officeFixture(t, map[string]string{
		"word/document.xml":             `<document><p><r><t>Text</t><endnoteReference id="9"/><footnoteReference id="2"/><endnoteReference id="9"/><footnoteReference id="404"/></r></p></document>`,
		"word/endnotes.xml":             `<endnotes><endnote id="9"><p><r><rPr><b/></rPr><t>End note</t></r></p></endnote></endnotes>`,
		"word/footnotes.xml":            `<footnotes><footnote id="0" type="separator"><p><r><t>Separator</t></r></p></footnote><footnote id="3"><p><r><t>Unused</t></r></p></footnote><footnote id="2"><p><hyperlink id="link"><r><t>Foot note</t></r></hyperlink></p></footnote></footnotes>`,
		"word/_rels/footnotes.xml.rels": `<Relationships><Relationship Id="link" Type="x/hyperlink" Target="https://example.com/source"/></Relationships>`,
	})
	d, _, err := DocxPreview("notes.docx", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Notes) != 3 || d.Notes[0].Blocks[0].Text() != "End note" || !d.Notes[0].Blocks[0].Runs[0].Bold || d.Notes[1].Blocks[0].Runs[0].Link != "https://example.com/source" {
		t.Fatalf("notes lost formatting or source: %+v", d.Notes)
	}
	r := d.Blocks[0].Runs
	if len(r) != 5 || r[1].NoteNumber != 1 || r[2].NoteNumber != 2 || r[3].NoteNumber != 3 || r[1].NoteReference == r[3].NoteReference || r[4].Text != "[Missing note]" {
		t.Fatalf("bad references: %+v", r)
	}
	if d.Notes[0].Reference != r[1].NoteReference {
		t.Fatal("backlink is not the first reference")
	}
}
func TestDocxNotesRejectMalformedAndOversizedParts(t *testing.T) {
	for _, notes := range []string{`<footnotes><footnote>`, strings.Repeat("x", (2<<20)+1), `<footnotes><footnote id="1"/><footnote id="1"/></footnotes>`} {
		_, _, err := DocxPreview("notes.docx", officeFixture(t, map[string]string{"word/document.xml": `<document/>`, "word/footnotes.xml": notes}))
		if err == nil {
			t.Fatal("accepted invalid note part")
		}
	}
}

func TestDocxRepeatedNotesBoundOutput(t *testing.T) {
	for _, fixture := range []struct {
		refs int
		text string
	}{{1001, "small"}, {100, strings.Repeat("x", 30000)}} {
		_, _, err := DocxPreview("notes.docx", officeFixture(t, map[string]string{
			"word/document.xml":  "<document><p><r>" + strings.Repeat(`<footnoteReference id="1"/>`, fixture.refs) + "</r></p></document>",
			"word/footnotes.xml": `<footnotes><footnote id="1"><p><r><t>` + fixture.text + `</t></r></p></footnote></footnotes>`,
		}))
		if err == nil {
			t.Fatal("unbounded repeated note output")
		}
	}
}
