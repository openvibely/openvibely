package spreadsheetpreviewindex

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxRows          = 10001
	maxColumns       = 100
	maxSheetRefs     = 100000
	maxWorkbookRefs  = 200000
	maxIndexBytes    = 16 << 20
	stringsIndexName = ".spreadsheet-strings.json"
)

var ErrIndexTooLarge = errors.New("spreadsheet preview string index exceeds limit")

// SidecarPath returns the private preview index path associated with an upload.
func SidecarPath(workbookPath string) string { return workbookPath + stringsIndexName }

type sheetPath struct {
	path string
}

// BuildJob tracks an asynchronous preview-index build.
type BuildJob struct {
	done chan struct{}
	err  error
}

func (job *BuildJob) Done() <-chan struct{} { return job.done }

func (job *BuildJob) Err() error {
	<-job.done
	return job.err
}

var buildJobs sync.Map

// StartBuild starts one deduplicated sidecar build per workbook path. Callers
// can return while a legacy workbook is being indexed instead of blocking the
// preview request on a full SST scan.
func StartBuild(workbookPath string) (*BuildJob, bool) {
	return startBuild(workbookPath, Build)
}

func startBuild(workbookPath string, build func(string) error) (*BuildJob, bool) {
	key, err := filepath.Abs(workbookPath)
	if err != nil {
		key = workbookPath
	}
	job := &BuildJob{done: make(chan struct{})}
	actual, loaded := buildJobs.LoadOrStore(key, job)
	if loaded {
		return actual.(*BuildJob), false
	}
	go func() {
		job.err = build(workbookPath)
		close(job.done)
		if job.err == nil {
			buildJobs.CompareAndDelete(key, job)
		} else {
			time.AfterFunc(time.Minute, func() { buildJobs.CompareAndDelete(key, job) })
		}
	}()
	return job, true
}

// Build creates a bounded sidecar containing shared strings referenced by cells
// in the spreadsheet preview region. It is intended to run after an upload is
// stored and before the file can be opened in the browser.
func Build(workbookPath string) error {
	_ = os.Remove(SidecarPath(workbookPath))
	archive, err := zip.OpenReader(workbookPath)
	if err != nil {
		return err
	}
	defer archive.Close()
	var shared *zip.File
	entries := make(map[string]*zip.File, len(archive.File))
	for _, file := range archive.File {
		name := strings.ToLower(path.Clean(filepath.ToSlash(file.Name)))
		entries[name] = file
		if name == "xl/sharedstrings.xml" {
			shared = file
		}
	}
	sheets := workbookOrderedSheets(entries)
	if shared == nil || len(sheets) == 0 {
		return writeIndex(workbookPath, map[string]string{})
	}
	refs := make(map[int]struct{})
	cellsScanned := 0
	for _, sheet := range sheets {
		if cellsScanned >= maxWorkbookRefs {
			break
		}
		reader, openErr := sheet.Open()
		if openErr != nil {
			return openErr
		}
		limit := maxSheetRefs
		if remaining := maxWorkbookRefs - cellsScanned; limit > remaining {
			limit = remaining
		}
		found, scanned, parseErr := previewStringRefs(reader, limit)
		closeErr := reader.Close()
		if parseErr != nil {
			return fmt.Errorf("reading worksheet preview references: %w", parseErr)
		}
		if closeErr != nil {
			return closeErr
		}
		cellsScanned += scanned
		for index := range found {
			refs[index] = struct{}{}
		}
	}
	if len(refs) == 0 {
		return writeIndex(workbookPath, map[string]string{})
	}
	reader, err := shared.Open()
	if err != nil {
		return err
	}
	values, err := selectedValues(reader, refs)
	closeErr := reader.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return writeIndex(workbookPath, values)
}

func writeIndex(workbookPath string, values map[string]string) error {
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if len(encoded) > maxIndexBytes {
		return ErrIndexTooLarge
	}
	indexPath := SidecarPath(workbookPath)
	tmp, err := os.CreateTemp(filepath.Dir(indexPath), ".spreadsheet-strings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpName, indexPath); err != nil {
		return err
	}
	return nil
}

func workbookOrderedSheets(entries map[string]*zip.File) []*zip.File {
	workbookFile := entries["xl/workbook.xml"]
	relsFile := entries["xl/_rels/workbook.xml.rels"]
	var ordered []*zip.File
	seen := make(map[string]struct{})
	if workbookFile != nil && relsFile != nil {
		workbook, workbookErr := workbookFile.Open()
		rels, relsErr := relsFile.Open()
		if workbookErr == nil && relsErr == nil {
			targets := workbookRelationshipTargets(rels)
			ordered = workbookSheetOrder(workbook, targets, entries, seen)
		}
		if workbook != nil {
			_ = workbook.Close()
		}
		if rels != nil {
			_ = rels.Close()
		}
	}
	remaining := make([]string, 0)
	for name := range entries {
		if strings.HasPrefix(name, "xl/worksheets/") && strings.HasSuffix(name, ".xml") && !strings.Contains(name, "_rels/") {
			if _, ok := seen[name]; !ok {
				remaining = append(remaining, name)
			}
		}
	}
	sort.Strings(remaining)
	for _, name := range remaining {
		ordered = append(ordered, entries[name])
	}
	return ordered
}

func workbookRelationshipTargets(r io.Reader) map[string]string {
	decoder := xml.NewDecoder(io.LimitReader(r, 4<<20))
	targets := make(map[string]string)
	for {
		token, err := decoder.Token()
		if err != nil {
			return targets
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		id, target := "", ""
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "Id":
				id = attr.Value
			case "Target":
				target = attr.Value
			}
		}
		if id != "" && target != "" {
			targets[id] = target
		}
	}
}

func workbookSheetOrder(r io.Reader, targets map[string]string, entries map[string]*zip.File, seen map[string]struct{}) []*zip.File {
	decoder := xml.NewDecoder(io.LimitReader(r, 4<<20))
	var ordered []*zip.File
	for {
		token, err := decoder.Token()
		if err != nil {
			return ordered
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			continue
		}
		relationshipID := ""
		for _, attr := range start.Attr {
			if attr.Name.Local == "id" {
				relationshipID = attr.Value
				break
			}
		}
		target := targets[relationshipID]
		if target == "" {
			continue
		}
		var name string
		if strings.HasPrefix(target, "/") {
			name = strings.TrimPrefix(path.Clean(target), "/")
		} else {
			name = path.Clean(path.Join("xl", target))
		}
		name = strings.ToLower(name)
		if file := entries[name]; file != nil {
			if _, ok := seen[name]; !ok {
				ordered = append(ordered, file)
				seen[name] = struct{}{}
			}
		}
	}
}

func previewStringRefs(r io.Reader, limit int) (map[int]struct{}, int, error) {
	refs := make(map[int]struct{})
	decoder := xml.NewDecoder(r)
	cellCount := 0
	for cellCount < limit {
		token, err := decoder.Token()
		if err == io.EOF {
			return refs, cellCount, nil
		}
		if err != nil {
			return nil, cellCount, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "c" {
			continue
		}
		coord, kind := "", ""
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "r":
				coord = attr.Value
			case "t":
				kind = attr.Value
			}
		}
		row, col, valid := cellCoordinate(coord)
		if !valid || row < 1 || row > maxRows || col < 0 || col >= maxColumns {
			continue
		}
		cellCount++
		if kind != "s" {
			continue
		}
		var cell struct {
			Value string `xml:"v"`
		}
		if err := decoder.DecodeElement(&cell, &start); err != nil {
			return nil, cellCount, err
		}
		index, err := strconv.Atoi(strings.TrimSpace(cell.Value))
		if err != nil || index < 0 {
			continue
		}
		refs[index] = struct{}{}
	}
	return refs, cellCount, nil
}

func cellCoordinate(ref string) (row, col int, ok bool) {
	pos := 0
	for pos < len(ref) && ((ref[pos] >= 'A' && ref[pos] <= 'Z') || (ref[pos] >= 'a' && ref[pos] <= 'z')) {
		letter := ref[pos]
		if letter >= 'a' {
			letter -= 'a' - 'A'
		}
		col = col*26 + int(letter-'A'+1)
		pos++
	}
	if pos == 0 || pos == len(ref) {
		return 0, 0, false
	}
	row, err := strconv.Atoi(ref[pos:])
	if err != nil {
		return 0, 0, false
	}
	return row, col - 1, true
}

type sharedItem struct {
	Text string `xml:"t"`
	Runs []struct {
		Text string `xml:"t"`
	} `xml:"r"`
}

func selectedValues(r io.Reader, wanted map[int]struct{}) (map[string]string, error) {
	values := make(map[string]string, len(wanted))
	estimatedBytes := 2
	decoder := xml.NewDecoder(r)
	index := 0
	for len(values) < len(wanted) {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "si" {
			continue
		}
		var item sharedItem
		if err := decoder.DecodeElement(&item, &start); err != nil {
			return nil, err
		}
		if _, needed := wanted[index]; needed {
			var textBuilder strings.Builder
			textBuilder.Grow(len(item.Text))
			textBuilder.WriteString(item.Text)
			for _, run := range item.Runs {
				textBuilder.WriteString(run.Text)
			}
			text := textBuilder.String()
			key := strconv.Itoa(index)
			estimatedBytes += len(key) + jsonStringSize(text) + 6
			if estimatedBytes > maxIndexBytes {
				return nil, ErrIndexTooLarge
			}
			values[key] = text
		}
		index++
	}
	return values, nil
}

func jsonStringSize(value string) int {
	size := 2
	for _, char := range []byte(value) {
		switch {
		case char == '"' || char == '\\':
			size += 2
		case char < 0x20:
			size += 6
		default:
			size++
		}
	}
	return size
}

// Read returns only requested entries from a generated sidecar.
func Read(path string, indexes []int) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) > maxIndexBytes {
		return nil, ErrIndexTooLarge
	}
	var all map[string]string
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	selected := make(map[string]string, len(indexes))
	for _, index := range indexes {
		key := strconv.Itoa(index)
		if value, ok := all[key]; ok {
			selected[key] = value
		}
	}
	return selected, nil
}

// DecodeIndexes reads the compact integer list accepted by the preview route.
func DecodeIndexes(r io.Reader) ([]int, error) {
	data, err := io.ReadAll(io.LimitReader(r, 2<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, errors.New("shared-string index request is too large")
	}
	var indexes []int
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&indexes); err != nil {
		return nil, err
	}
	if len(indexes) > maxWorkbookRefs {
		return nil, errors.New("too many shared-string indexes")
	}
	for _, index := range indexes {
		if index < 0 {
			return nil, errors.New("invalid shared-string index")
		}
	}
	return indexes, nil
}
