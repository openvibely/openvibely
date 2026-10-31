package components

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestDiffChunksPreserveLinesAndSplitPairs(t *testing.T) {
	file := DiffFile{Path: "big.go"}
	hunk := DiffHunk{Header: "@@ -1,601 +1,501 @@"}
	for i := 1; i <= 600; i++ {
		hunk.Lines = append(hunk.Lines, DiffLine{Type: "del", Content: fmt.Sprint(i), OldNum: i})
	}
	for i := 1; i <= 500; i++ {
		hunk.Lines = append(hunk.Lines, DiffLine{Type: "add", Content: fmt.Sprint(i), NewNum: i})
	}
	hunk.Lines = append(hunk.Lines, DiffLine{Type: "del", Content: "another old", OldNum: 601}, DiffLine{Type: "add", Content: "another new", NewNum: 501})
	hunk.Lines = append(hunk.Lines, DiffLine{Type: "ctx", Content: "end", OldNum: 602, NewNum: 502})
	file.Hunks = []DiffHunk{hunk}
	for _, view := range []string{"inline", "split"} {
		t.Run(view, func(t *testing.T) {
			chunks := DiffFileChunks(file, view)
			if len(chunks) < 3 {
				t.Fatal("expected several bounded chunks")
			}
			var lines []DiffLine
			var pairs []SplitPair
			for _, chunk := range chunks {
				count := 0
				for _, h := range chunk.Hunks {
					lines = append(lines, h.Lines...)
					p := splitHunkLines(h)
					pairs = append(pairs, p...)
					if view == "split" {
						count += len(p)
					} else {
						count += len(h.Lines)
					}
				}
				if count > diffChunkRows {
					t.Fatalf("unbounded chunk: %d", count)
				}
			}
			if view == "inline" && !reflect.DeepEqual(lines, hunk.Lines) {
				t.Fatal("inline lines lost or reordered")
			}
			if view == "split" && !reflect.DeepEqual(pairs, buildSplitLines(hunk.Lines)) {
				t.Fatal("split alignment changed across chunk boundary")
			}
		})
	}
}

func TestProgressiveDiffIncludesEveryFileWithoutBodies(t *testing.T) {
	var diff strings.Builder
	for i := 0; i < 350; i++ {
		fmt.Fprintf(&diff, "diff --git a/f%d b/f%d\n--- a/f%d\n+++ b/f%d\n@@ -0,0 +1 @@\n+hello\n", i, i, i, i)
	}
	var out bytes.Buffer
	if err := DiffViewer(diff.String(), "task", nil, "inline").Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `data-diff-path="f349"`) || strings.Contains(out.String(), "<tr") {
		t.Fatal("manifest must contain all files without eagerly rendering rows")
	}
	if strings.Contains(out.String(), `class="diff-file-card `) {
		t.Fatal("initial response still contains file cards")
	}
	if out.Len() > 256*1024 {
		t.Fatalf("350-file index response is too large: %d bytes", out.Len())
	}
	t.Logf("350-file initial HTML: %d bytes, no file cards", out.Len())
	if _, ok := DiffRenderMetaByIndex(diff.String(), 349); !ok {
		t.Fatal("file after former cap is inaccessible")
	}
}

func TestDiffFileRevisionScopesComments(t *testing.T) {
	f := DiffFile{Path: "a"}
	revision := DiffFileRevision(f, nil)
	if revision != DiffFileRevision(f, []models.ReviewComment{{FilePath: "b", CommentText: "other"}}) {
		t.Fatal("unrelated review invalidated file")
	}
	if revision == DiffFileRevision(f, []models.ReviewComment{{FilePath: "a", CommentText: "review"}}) {
		t.Fatal("review did not invalidate file")
	}
}

func TestChunkReviewAtBoundary(t *testing.T) {
	file := DiffFile{Path: "review.go", Hunks: []DiffHunk{{Header: "@@ -0,0 +1,401 @@"}}}
	for i := 1; i <= 401; i++ {
		file.Hunks[0].Lines = append(file.Hunks[0].Lines, DiffLine{Type: "add", Content: "line", NewNum: i})
	}
	comments := []models.ReviewComment{{ID: "boundary-review", FilePath: file.Path, LineNumber: 201, LineType: "new", CommentText: "Keep this review"}}
	for _, view := range []string{"inline", "split"} {
		chunks := DiffFileChunks(file, view)
		for index, chunk := range chunks {
			var out bytes.Buffer
			if err := DiffChunk(chunk, 0, view, "task", comments, true, index, len(chunks), DiffFileRevision(file, comments)).Render(context.Background(), &out); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "boundary-review") != (index == 1) {
				t.Fatalf("%s: review attached to wrong chunk %d", view, index)
			}
		}
	}
}

func TestDiffChunkContainingLine(t *testing.T) {
	file := DiffFile{Path: "large", Hunks: []DiffHunk{{Header: "replacement"}}}
	for i := 1; i <= 1000; i++ {
		file.Hunks[0].Lines = append(file.Hunks[0].Lines, DiffLine{Type: "del", OldNum: i}, DiffLine{Type: "add", NewNum: i})
	}
	for _, view := range []string{"inline", "split"} {
		chunks := DiffFileChunks(file, view)
		expected := 4
		if view == "split" {
			expected = 2
		}
		for _, anchor := range [][2]int{{401, 0}, {0, 401}, {1, 401}, {401, 9999}} {
			index, found := DiffChunkContainingLine(chunks, anchor[0], anchor[1])
			if !found || index != expected {
				t.Fatalf("%s anchor %v: got %d,%t want %d", view, anchor, index, found, expected)
			}
		}
		if _, found := DiffChunkContainingLine(chunks, 0, 9999); found {
			t.Fatal("missing line matched")
		}
		if _, found := DiffChunkContainingLine(chunks, 0, 0); found {
			t.Fatal("empty anchor matched")
		}
	}
}

func TestDiffCodeRevisionExcludesReviewAnnotations(t *testing.T) {
	file := ParseDiffOutput("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -0,0 +1 @@\n+code\n")[0]
	before := progressiveDiffMetas([]DiffFile{file}, nil)[0]
	for _, text := range []string{"new comment", "edited comment"} {
		after := progressiveDiffMetas([]DiffFile{file}, []models.ReviewComment{{FilePath: "a", CommentText: text}})[0]
		if before.CodeRevision != after.CodeRevision {
			t.Fatal("review changed chunk identity")
		}
		if before.Revision == after.Revision {
			t.Fatal("review no longer invalidates displayed card")
		}
		var output bytes.Buffer
		if err := diffViewerWithReviewContent([]DiffFileRenderMeta{after}, "task", nil, "inline", "viewer-revision").Render(context.Background(), &output); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), `data-code-revision="`+before.CodeRevision+`"`) {
			t.Fatal("file index omitted code identity")
		}
	}
	file.Hunks[0].Lines[0].Content = "different code"
	if DiffCodeRevision(file) == before.CodeRevision {
		t.Fatal("code changes must invalidate chunks")
	}
}

func TestDiffFileTooLarge(t *testing.T) {
	file := func(lines int, content string) DiffFile {
		hunk := DiffHunk{}
		for i := 0; i < lines; i++ {
			hunk.Lines = append(hunk.Lines, DiffLine{Type: "add", Content: content})
		}
		return DiffFile{Path: "f", Hunks: []DiffHunk{hunk}}
	}
	if DiffFileTooLarge(file(maxDiffFileLines, "x")) {
		t.Fatal("line limit should be inclusive")
	}
	if !DiffFileTooLarge(file(maxDiffFileLines+1, "x")) {
		t.Fatal("over line limit not detected")
	}
	if DiffFileTooLarge(file(1, strings.Repeat("x", maxDiffFileBytes))) {
		t.Fatal("byte limit should be inclusive")
	}
	if !DiffFileTooLarge(file(1, strings.Repeat("x", maxDiffFileBytes+1))) {
		t.Fatal("over byte limit not detected")
	}
}

func TestDiffViewerMarksOversizedFiles(t *testing.T) {
	diff := "diff --git a/small.txt b/small.txt\n--- a/small.txt\n+++ b/small.txt\n@@ -0,0 +1 @@\n+ok\n" +
		"diff --git a/huge.txt b/huge.txt\n--- a/huge.txt\n+++ b/huge.txt\n@@ -0,0 +1,50001 @@\n" + strings.Repeat("+x\n", maxDiffFileLines+1)
	metas := progressiveDiffMetas(ParseDiffOutput(diff), nil)
	for _, taskID := range []string{"task", ""} {
		var output strings.Builder
		if err := diffViewerWithReviewContent(metas, taskID, nil, "inline", "rev").Render(context.Background(), &output); err != nil {
			t.Fatal(err)
		}
		body := output.String()
		if strings.Count(body, `data-diff-too-large="true"`) != map[string]int{"task": 1, "": 3}[taskID] {
			t.Fatalf("taskID=%q: unexpected too-large markers", taskID)
		}
		if taskID == "" && (!strings.Contains(body, "Diff too large to display") || strings.Contains(body, ">+x<")) {
			t.Fatal("inline fallback rendered oversized content")
		}
	}
}
