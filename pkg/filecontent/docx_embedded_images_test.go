package filecontent

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

const benchmarkDocxImageTargets = 2000

var benchmarkPNG = []byte{
	0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0b, 'I', 'D', 'A', 'T',
	0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0xf0, 0x1f,
	0x00, 0x05, 0x00, 0x01, 0xff, 0x89, 0x99, 0x3d,
	0x1d, 0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D',
	0xae, 'B', '`', '\x82',
}

type docxBenchmarkEntry struct {
	name string
	data []byte
}

func makeDocxEmbeddedImagesBenchmarkFixture(targetCount, distinctImageCount, entryCount int) []byte {
	if distinctImageCount < 0 || distinctImageCount > targetCount || entryCount < 2+distinctImageCount {
		panic("invalid DOCX benchmark fixture dimensions")
	}

	var relationships strings.Builder
	relationships.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	var document strings.Builder
	document.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"><w:body><w:p><w:r><w:t>benchmark preview</w:t></w:r>`)
	for i := 0; i < targetCount; i++ {
		id := fmt.Sprintf("image%d", i)
		partIndex := i
		if distinctImageCount > 0 {
			partIndex %= distinctImageCount
		}
		part := fmt.Sprintf("image%d.png", partIndex)
		fmt.Fprintf(&relationships, `<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/%s"/>`, id, part)
	}
	relationships.WriteString(`</Relationships>`)
	document.WriteString(`<w:r><w:drawing><wp:inline><wp:docPr descr="benchmark image"/><a:blip r:embed="image0"/></wp:inline></w:drawing></w:r></w:p><w:sectPr/></w:body></w:document>`)

	entries := make([]docxBenchmarkEntry, 0, entryCount)
	entries = append(entries,
		docxBenchmarkEntry{name: "word/_rels/document.xml.rels", data: []byte(relationships.String())},
		docxBenchmarkEntry{name: "word/document.xml", data: []byte(document.String())},
	)
	for i := 0; i < distinctImageCount; i++ {
		entries = append(entries, docxBenchmarkEntry{name: fmt.Sprintf("word/media/image%d.png", i), data: benchmarkPNG})
	}
	for i := len(entries); i < entryCount; i++ {
		entries = append(entries, docxBenchmarkEntry{name: fmt.Sprintf("extras/part-%04d.xml", i), data: []byte(`<part/>`)})
	}

	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, entry := range entries {
		file, err := writer.Create(entry.name)
		if err != nil {
			panic(err)
		}
		if _, err := file.Write(entry.data); err != nil {
			panic(err)
		}
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return archive.Bytes()
}

func docxFixtureWithEntries(t *testing.T, entries ...docxBenchmarkEntry) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, entry := range entries {
		file, err := writer.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestDocxPreviewEmbeddedImagesPreservesTargetBehavior(t *testing.T) {
	relationships := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="present" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/picture.png"/>` +
		`<Relationship Id="presentAgain" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/picture.png"/>` +
		`<Relationship Id="missing" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/missing.png"/>` +
		`<Relationship Id="external" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" TargetMode="External" Target="https://example.com/picture.png"/>` +
		`</Relationships>`
	document := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><w:body><w:p><w:r><w:t>preview text</w:t></w:r>` +
		`<w:r><w:drawing><a:blip r:embed="present"/></w:drawing></w:r>` +
		`<w:r><w:drawing><a:blip r:embed="presentAgain"/></w:drawing></w:r>` +
		`<w:r><w:drawing><a:blip r:embed="missing"/></w:drawing></w:r>` +
		`<w:r><w:drawing><a:blip r:embed="external"/></w:drawing></w:r>` +
		`</w:p></w:body></w:document>`
	fixture := docxFixtureWithEntries(t,
		docxBenchmarkEntry{name: "word/_rels/document.xml.rels", data: []byte(relationships)},
		docxBenchmarkEntry{name: "word/document.xml", data: []byte(document)},
		docxBenchmarkEntry{name: "word/media/picture.png", data: benchmarkPNG},
		docxBenchmarkEntry{name: "extras/unrelated.bin", data: []byte("one")},
		docxBenchmarkEntry{name: "extras/unrelated.bin", data: []byte("two")},
	)

	preview, supported, err := DocxPreview("images.docx", fixture)
	if err != nil || !supported {
		t.Fatalf("DocxPreview() supported=%v, err=%v", supported, err)
	}
	var text string
	var imageURIs []string
	for _, block := range preview.Blocks {
		for _, run := range block.Runs {
			text += run.Text
			if run.ImageDataURI != "" {
				imageURIs = append(imageURIs, run.ImageDataURI)
			}
		}
	}
	if text != "preview text" {
		t.Fatalf("preview text = %q, want %q", text, "preview text")
	}
	wantURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(benchmarkPNG)
	if len(imageURIs) != 2 || imageURIs[0] != wantURI || imageURIs[1] != wantURI {
		t.Fatalf("image data URIs = %q, want two %q", imageURIs, wantURI)
	}
}

func TestDocxPreviewEmbeddedImageDuplicateTargetIsAmbiguous(t *testing.T) {
	relationships := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="image" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/picture.png"/></Relationships>`
	document := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><w:body><w:p><w:r><w:drawing><a:blip r:embed="image"/></w:drawing></w:r></w:p></w:body></w:document>`
	fixture := docxFixtureWithEntries(t,
		docxBenchmarkEntry{name: "word/_rels/document.xml.rels", data: []byte(relationships)},
		docxBenchmarkEntry{name: "word/document.xml", data: []byte(document)},
		docxBenchmarkEntry{name: "word/media/picture.png", data: benchmarkPNG},
		docxBenchmarkEntry{name: "word/media/picture.png", data: benchmarkPNG},
	)

	if _, supported, err := DocxPreview("ambiguous.docx", fixture); err == nil || !supported || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("DocxPreview() supported=%v, err=%v, want ambiguous image error", supported, err)
	}
}

// BenchmarkDocxPreviewEmbeddedImages isolates ZIP target resolution in the image stage
// used by DocxPreview. Its unresolved targets avoid payload decoding so timings capture
// archive metadata lookup; each generated fixture is built before its timed loop.
func BenchmarkDocxPreviewEmbeddedImages(b *testing.B) {
	cases := []struct {
		name                         string
		targets, imageParts, entries int
	}{
		{name: "near_5000_entries", targets: benchmarkDocxImageTargets, imageParts: 0, entries: 4999},
		{name: "about_500_entries", targets: benchmarkDocxImageTargets, imageParts: 0, entries: 500}}
	for _, testCase := range cases {
		fixture := makeDocxEmbeddedImagesBenchmarkFixture(testCase.targets, testCase.imageParts, testCase.entries)
		archive, err := zip.NewReader(bytes.NewReader(fixture), int64(len(fixture)))
		if err != nil {
			b.Fatal(err)
		}
		b.Run(testCase.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := docxEmbeddedImages(archive); err != nil {
					b.Fatalf("docxEmbeddedImages() err=%v", err)
				}
			}
		})
	}
}
