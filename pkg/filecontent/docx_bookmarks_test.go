package filecontent

import (
	"strconv"
	"strings"
	"testing"
)

func TestDocxBookmarksAndHyperlinkFragments(t *testing.T) {
	d, _, err := DocxPreview("bookmarks.docx", officeFixture(t, map[string]string{
		"word/document.xml":            `<document><bookmarkStart name="target"/><p><hyperlink anchor="target"><r><t>Go</t></r></hyperlink><hyperlink id="external" anchor="part two"><r><t>Source</t></r></hyperlink><bookmarkStart name="target"/><bookmarkStart name="_GoBack"/><r><t>Destination</t></r></p></document>`,
		"word/_rels/document.xml.rels": `<Relationships><Relationship Id="external" Type="x/hyperlink" Target="https://example.com/doc#old"/></Relationships>`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	runs := d.Blocks[0].Runs
	if len(runs) != 4 || runs[0].BookmarkID == "" || runs[1].Link != "#"+runs[0].BookmarkID || runs[2].Link != "https://example.com/doc#part%20two" {
		t.Fatalf("bookmark/fragment not resolved: %+v", runs)
	}
	for _, name := range []string{"", "_GoBack", strings.Repeat("x", 513)} {
		if docxBookmarkID(name) != "" {
			t.Fatal("accepted invalid bookmark name")
		}
	}
	if id := docxBookmarkID(`"><script>bad</script>`); strings.ContainsAny(id, `<>"`) {
		t.Fatal("unsafe generated ID")
	}
}
func TestDocxBookmarkCountBounded(t *testing.T) {
	var body strings.Builder
	body.WriteString("<document><p>")
	for i := 0; i < 1001; i++ {
		body.WriteString(`<bookmarkStart name="` + "bookmark" + strconv.Itoa(i) + `"/>`)
	}
	body.WriteString("</p></document>")
	_, _, err := DocxPreview("many.docx", officeFixture(t, map[string]string{"word/document.xml": body.String()}))
	if err == nil || !strings.Contains(err.Error(), "bookmarks exceed") {
		t.Fatalf("unbounded bookmarks accepted or wrong rejection: %v", err)
	}
}
