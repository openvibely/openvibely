package filecontent

import (
	"testing"
)

func TestSVGAttachmentClassification(t *testing.T) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40"><rect width="40" height="40" fill="red"/></svg>`
	for _, bad := range []string{`<html/>`, svg + svg, `<!DOCTYPE svg>` + svg, `<svg xmlns="http://www.w3.org/2000/svg">`, svg + "trailing"} {
		if IsSVG("bad.svg", []byte(bad)) {
			t.Fatal("accepted invalid SVG", bad)
		}
	}
	if !IsSVG("ok.svg", []byte(svg)) {
		t.Fatal("valid SVG rejected")
	}
}
