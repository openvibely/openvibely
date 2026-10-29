package spreadsheetpreviewindex

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildUsesWorkbookSheetOrderForWorkbookCellCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ordered.xlsx")
	file, err := os.Create(path)
	require.NoError(t, err)
	archive := zip.NewWriter(file)
	for _, sheet := range []struct {
		name  string
		index int
	}{
		{"sheet2.xml", 0},
		{"sheet3.xml", 1},
		{"sheet1.xml", 2},
	} {
		entry, createErr := archive.Create("xl/worksheets/" + sheet.name)
		require.NoError(t, createErr)
		_, writeErr := fmt.Fprint(entry, `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1">`)
		require.NoError(t, writeErr)
		for i := 0; i < maxSheetRefs; i++ {
			_, writeErr = fmt.Fprintf(entry, `<c r="A1" t="s"><v>%d</v></c>`, sheet.index)
			require.NoError(t, writeErr)
		}
		_, writeErr = fmt.Fprint(entry, `</row></sheetData></worksheet>`)
		require.NoError(t, writeErr)
	}
	workbook, err := archive.Create("xl/workbook.xml")
	require.NoError(t, err)
	_, err = fmt.Fprint(workbook, `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="rId1"/><sheet name="Second" sheetId="2" r:id="rId2"/><sheet name="Third" sheetId="3" r:id="rId3"/></sheets></workbook>`)
	require.NoError(t, err)
	rels, err := archive.Create("xl/_rels/workbook.xml.rels")
	require.NoError(t, err)
	_, err = fmt.Fprint(rels, `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Target="worksheets/sheet3.xml"/></Relationships>`)
	require.NoError(t, err)
	shared, err := archive.Create("xl/sharedStrings.xml")
	require.NoError(t, err)
	_, err = fmt.Fprint(shared, `<sst><si><t>Second</t></si><si><t>Third</t></si><si><t>First</t></si></sst>`)
	require.NoError(t, err)
	require.NoError(t, archive.Close())
	require.NoError(t, file.Close())

	require.NoError(t, Build(path))
	values, err := Read(SidecarPath(path), []int{0, 1, 2})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"0": "Second", "2": "First"}, values)
}

func TestStartBuildRunsAsynchronouslyAndDeduplicatesConcurrentRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.xlsx")
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	first, startedBuild := startBuild(path, func(string) error {
		calls.Add(1)
		close(started)
		<-release
		return nil
	})
	require.True(t, startedBuild)
	select {
	case <-first.Done():
		t.Fatal("build should not block the caller")
	default:
	}
	<-started
	second, secondStarted := startBuild(path, func(string) error {
		calls.Add(1)
		return nil
	})
	require.False(t, secondStarted)
	require.Same(t, first, second)
	close(release)
	<-first.Done()
	require.NoError(t, first.Err())
	require.EqualValues(t, 1, calls.Load())
}

func TestBuildIndexesOnlyPreviewReferencedSharedStrings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.xlsx")
	file, err := os.Create(path)
	require.NoError(t, err)
	archive := zip.NewWriter(file)
	worksheet, err := archive.Create("xl/worksheets/sheet1.xml")
	require.NoError(t, err)
	_, err = fmt.Fprint(worksheet, `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="B1" t="s"><v>4001</v></c><c r="XFD1" t="s"><v>3</v></c></row></sheetData></worksheet>`)
	require.NoError(t, err)
	shared, err := archive.Create("xl/sharedStrings.xml")
	require.NoError(t, err)
	_, err = fmt.Fprint(shared, `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	require.NoError(t, err)
	encoder := xml.NewEncoder(shared)
	for index := 0; index <= 4001; index++ {
		value := fmt.Sprintf("unused-%d", index)
		if index == 4001 {
			value = "Cold high-index exact value"
		}
		item := struct {
			XMLName xml.Name `xml:"si"`
			Text    string   `xml:"t"`
		}{Text: value}
		require.NoError(t, encoder.Encode(item))
	}
	require.NoError(t, encoder.Flush())
	_, err = fmt.Fprint(shared, `</sst>`)
	require.NoError(t, err)
	require.NoError(t, archive.Close())
	require.NoError(t, file.Close())

	require.NoError(t, Build(path))
	values, err := Read(SidecarPath(path), []int{4001, 3, 99999})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"4001": "Cold high-index exact value"}, values)
}

func TestPreviewStringRefsInfersMissingCoordinates(t *testing.T) {
	for _, tt := range []struct {
		name, xml string
		want      map[int]struct{}
	}{
		{"implicit rows and cells", `<row><c t="s"><v>0</v></c><c t="s"><v>1</v></c></row><row><c t="s"><v>2</v></c></row>`, map[int]struct{}{0: {}, 1: {}, 2: {}}},
		{"explicit then implicit columns", `<row r="1"><c r="CU1" t="s"><v>0</v></c><c t="s"><v>1</v></c><c t="s"><v>2</v></c></row>`, map[int]struct{}{0: {}, 1: {}}},
		{"explicit then implicit rows", `<row r="10001"><c t="s"><v>0</v></c></row><row><c t="s"><v>1</v></c></row>`, map[int]struct{}{0: {}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			refs, _, err := previewStringRefs(strings.NewReader("<worksheet><sheetData>"+tt.xml+"</sheetData></worksheet>"), maxSheetRefs)
			require.NoError(t, err)
			require.Equal(t, tt.want, refs)
		})
	}
}

func TestBuildIndexesCellsWithoutCoordinates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "implicit.xlsx")
	file, err := os.Create(path)
	require.NoError(t, err)
	archive := zip.NewWriter(file)
	for name, content := range map[string]string{
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row><c t="s"><v>0</v></c></row><row><c t="s"><v>1</v></c></row></sheetData></worksheet>`,
		"xl/sharedStrings.xml":     `<sst><si><t>First</t></si><si><t>Second</t></si></sst>`,
	} {
		entry, err := archive.Create(name)
		require.NoError(t, err)
		_, err = fmt.Fprint(entry, content)
		require.NoError(t, err)
	}
	require.NoError(t, archive.Close())
	require.NoError(t, file.Close())
	require.NoError(t, Build(path))
	values, err := Read(SidecarPath(path), []int{0, 1})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"0": "First", "1": "Second"}, values)
}
