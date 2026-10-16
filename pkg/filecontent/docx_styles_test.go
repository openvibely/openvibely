package filecontent

import (
	"strings"
	"testing"
)

func TestDocxNamedStylesMatchDefaultMappings(t *testing.T) {
	styles := `<w:styles xmlns:w="urn:test"><w:style w:type="paragraph" w:styleId="custom"><w:name w:val="Heading 2"/></w:style><w:style w:type="character" w:styleId="named-bold"><w:name w:val="Strong"/></w:style><w:style w:type="character" w:styleId="custom"><w:name w:val="Strong"/></w:style></w:styles>`
	content := `<w:document xmlns:w="urn:test"><w:body><w:p><w:pPr><w:pStyle w:val="custom"/></w:pPr><w:r><w:rPr><w:rStyle w:val="named-bold"/><w:b w:val="0"/></w:rPr><w:t>Heading text</w:t></w:r><w:r><w:t> plain</w:t></w:r></w:p><w:p><w:pPr><w:pStyle w:val="Heading"/></w:pPr><w:r><w:t>Top heading</w:t></w:r></w:p></w:body></w:document>`
	result, _, err := DocxPreview("styles.docx", officeFixture(t, map[string]string{"word/document.xml": content, "word/styles.xml": styles}))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Blocks) != 2 || result.Blocks[0].Kind != "heading2" || result.Blocks[1].Kind != "heading1" {
		t.Fatalf("lost named headings: %+v", result)
	}
	runs := result.Blocks[0].Runs
	if len(runs) != 2 || !runs[0].Bold || runs[1].Bold {
		t.Fatalf("named strong style missing or leaked: %+v", runs)
	}
}
func TestDocxStyleLimits(t *testing.T) {
	for _, styles := range []string{`<styles><style>`, "<styles>" + strings.Repeat("<x>", 65) + strings.Repeat("</x>", 65) + "</styles>", `<styles>` + strings.Repeat(`<style type="paragraph" styleId="x"/>`, 4097) + `</styles>`, strings.Repeat(" ", (1<<20)+1)} {
		_, _, err := DocxPreview("styles.docx", officeFixture(t, map[string]string{"word/document.xml": `<document/>`, "word/styles.xml": styles}))
		if err == nil {
			t.Fatal("accepted invalid/unbounded style table")
		}
	}
}
