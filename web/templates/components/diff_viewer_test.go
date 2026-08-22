package components

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestParseDiffOutput_Empty(t *testing.T) {
	files := ParseDiffOutput("")
	if len(files) != 0 {
		t.Errorf("expected 0 files for empty diff, got %d", len(files))
	}
}

func TestParseDiffOutput_SingleFile(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
index abc1234..def5678 100644
--- a/main.go
+++ b/main.go
@@ -1,5 +1,6 @@
 package main

+import "fmt"
+
 func main() {
-	println("hello")
+	fmt.Println("hello")
 }
`
	files := ParseDiffOutput(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != "main.go" {
		t.Errorf("expected path=main.go, got %q", files[0].Path)
	}
	if len(files[0].Hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(files[0].Hunks))
	}

	hunk := files[0].Hunks[0]
	addCount, delCount := 0, 0
	for _, line := range hunk.Lines {
		if line.Type == "add" {
			addCount++
		} else if line.Type == "del" {
			delCount++
		}
	}
	if addCount != 3 {
		t.Errorf("expected 3 additions, got %d", addCount)
	}
	if delCount != 1 {
		t.Errorf("expected 1 deletion, got %d", delCount)
	}
}

func TestParseDiffOutput_MultipleFiles(t *testing.T) {
	diff := `diff --git a/file1.go b/file1.go
--- a/file1.go
+++ b/file1.go
@@ -1,3 +1,4 @@
 package pkg
+import "fmt"
 func A() {
 }
diff --git a/file2.go b/file2.go
--- a/file2.go
+++ b/file2.go
@@ -1,3 +1,3 @@
 package pkg
-func B() {
+func B(x int) {
 }
`
	files := ParseDiffOutput(diff)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Path != "file1.go" {
		t.Errorf("expected first file=file1.go, got %q", files[0].Path)
	}
	if files[1].Path != "file2.go" {
		t.Errorf("expected second file=file2.go, got %q", files[1].Path)
	}
}

func TestParseDiffOutput_NewFile(t *testing.T) {
	diff := `diff --git a/newfile.go b/newfile.go
new file mode 100644
--- /dev/null
+++ b/newfile.go
@@ -0,0 +1,3 @@
+package main
+
+func New() {}
`
	files := ParseDiffOutput(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != "newfile.go" {
		t.Errorf("expected path=newfile.go, got %q", files[0].Path)
	}
	if len(files[0].Hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(files[0].Hunks))
	}
	// Count additions - all content lines should be additions
	addCount := 0
	for _, line := range files[0].Hunks[0].Lines {
		if line.Type == "add" {
			addCount++
		}
	}
	if addCount != 3 {
		t.Errorf("expected 3 additions, got %d", addCount)
	}
}

func TestParseDiffOutput_WithLegacyUntrackedComments(t *testing.T) {
	// Legacy format: untracked files listed as comments should be ignored
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }

# Untracked files:
# + newfile.go
# + another.txt
`
	files := ParseDiffOutput(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (legacy untracked comments should be ignored), got %d", len(files))
	}
}

func TestParseDiffOutput_NewUntrackedFileFormat(t *testing.T) {
	// New format: untracked files are proper unified diffs
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }

diff --git a/newfile.txt b/newfile.txt
new file mode 100644
--- /dev/null
+++ b/newfile.txt
@@ -0,0 +1,2 @@
+hello world
+second line
`
	files := ParseDiffOutput(diff)
	if len(files) != 2 {
		t.Fatalf("expected 2 files (modified + new), got %d", len(files))
	}
	if files[0].Path != "main.go" {
		t.Errorf("expected first file=main.go, got %q", files[0].Path)
	}
	if files[1].Path != "newfile.txt" {
		t.Errorf("expected second file=newfile.txt, got %q", files[1].Path)
	}
	// Verify new file has all additions
	addCount := 0
	for _, hunk := range files[1].Hunks {
		for _, line := range hunk.Lines {
			if line.Type == "add" {
				addCount++
			}
		}
	}
	if addCount != 2 {
		t.Errorf("expected 2 additions in new file, got %d", addCount)
	}
}

func TestParseDiffOutput_PureRename_NoContentChange(t *testing.T) {
	diff := `diff --git a/old.txt b/renamed.txt
similarity index 100%
rename from old.txt
rename to renamed.txt
`
	files := ParseDiffOutput(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Status != "renamed" {
		t.Errorf("expected status=renamed, got %q", f.Status)
	}
	if f.OldPath != "old.txt" {
		t.Errorf("expected OldPath=old.txt, got %q", f.OldPath)
	}
	if f.Path != "renamed.txt" {
		t.Errorf("expected Path=renamed.txt, got %q", f.Path)
	}
	if f.SimilarityIndex != 100 {
		t.Errorf("expected SimilarityIndex=100, got %d", f.SimilarityIndex)
	}
	if diffHasTextualContent(f) {
		t.Errorf("expected no textual content for pure rename")
	}
}

func TestParseDiffOutput_QuotedRenamePath(t *testing.T) {
	diff := "diff --git \"a/old\\tname.txt\" \"b/new\\tname.txt\"\n" +
		"similarity index 100%\n" +
		"rename from \"old\\tname.txt\"\n" +
		"rename to \"new\\tname.txt\"\n"
	files := ParseDiffOutput(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Status != "renamed" {
		t.Errorf("expected status=renamed, got %q", f.Status)
	}
	if f.OldPath != "old\tname.txt" {
		t.Errorf("expected decoded OldPath, got %q", f.OldPath)
	}
	if f.Path != "new\tname.txt" {
		t.Errorf("expected decoded Path, got %q", f.Path)
	}
}

func TestParseDiffOutput_RenameWithContentChange(t *testing.T) {
	diff := `diff --git a/old.txt b/sub/new.txt
similarity index 76%
rename from old.txt
rename to sub/new.txt
index b3c5a95..b421ba1 100644
--- a/old.txt
+++ b/sub/new.txt
@@ -3,3 +3,4 @@ line2
 line3
 line4
 line5
+appended
`
	files := ParseDiffOutput(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Status != "renamed" {
		t.Errorf("expected status=renamed, got %q", f.Status)
	}
	if f.OldPath != "old.txt" || f.Path != "sub/new.txt" {
		t.Errorf("unexpected paths Old=%q New=%q", f.OldPath, f.Path)
	}
	if f.SimilarityIndex != 76 {
		t.Errorf("expected SimilarityIndex=76, got %d", f.SimilarityIndex)
	}
	if !diffHasTextualContent(f) {
		t.Errorf("expected textual content for rename+edit")
	}
	addCount := 0
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Type == "add" {
				addCount++
			}
		}
	}
	if addCount != 1 {
		t.Errorf("expected 1 addition in rename+edit, got %d", addCount)
	}
}

func TestParseDiffOutput_DeletedFile(t *testing.T) {
	diff := `diff --git a/b.txt b/b.txt
deleted file mode 100644
index 8616c68..0000000
--- a/b.txt
+++ /dev/null
@@ -1 +0,0 @@
-to-delete
`
	files := ParseDiffOutput(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Status != "deleted" {
		t.Errorf("expected status=deleted, got %q", f.Status)
	}
	if f.Path != "b.txt" {
		t.Errorf("expected Path=b.txt (preserved through +++ /dev/null), got %q", f.Path)
	}
	if f.OldPath != "b.txt" {
		t.Errorf("expected OldPath=b.txt, got %q", f.OldPath)
	}
}

func TestParseDiffOutput_DeletedBinaryFile(t *testing.T) {
	diff := `diff --git a/img.bin b/img.bin
deleted file mode 100644
index 2f80ba2..0000000
Binary files a/img.bin and /dev/null differ
`
	files := ParseDiffOutput(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Status != "deleted" {
		t.Errorf("expected status=deleted, got %q", f.Status)
	}
	if !f.IsBinary {
		t.Error("expected IsBinary=true")
	}
	if diffHasTextualContent(f) {
		t.Error("expected no textual hunks for deleted binary")
	}
	if f.Path != "img.bin" {
		t.Errorf("expected Path=img.bin, got %q", f.Path)
	}
}

func TestParseDiffOutput_AddedBinaryFile(t *testing.T) {
	diff := `diff --git a/img.bin b/img.bin
new file mode 100644
index 0000000..2f80ba2
Binary files /dev/null and b/img.bin differ
`
	files := ParseDiffOutput(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Status != "added" {
		t.Errorf("expected status=added, got %q", f.Status)
	}
	if !f.IsBinary {
		t.Error("expected IsBinary=true")
	}
}

func TestParseDiffOutput_NewFile_StatusAdded(t *testing.T) {
	diff := `diff --git a/d.txt b/d.txt
new file mode 100644
index 0000000..a1ea743
--- /dev/null
+++ b/d.txt
@@ -0,0 +1 @@
+fresh file
`
	files := ParseDiffOutput(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	f := files[0]
	if f.Status != "added" {
		t.Errorf("expected status=added, got %q", f.Status)
	}
	if f.OldPath != "" {
		t.Errorf("expected empty OldPath for added file, got %q", f.OldPath)
	}
	if f.Path != "d.txt" {
		t.Errorf("expected Path=d.txt, got %q", f.Path)
	}
}

func TestParseDiffOutput_PlainModification_StatusModified(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
index abc1234..def5678 100644
--- a/main.go
+++ b/main.go
@@ -1,2 +1,2 @@
 package main
-x
+y
`
	files := ParseDiffOutput(diff)
	if len(files) != 1 || files[0].Status != "modified" {
		t.Fatalf("expected single modified file, got %#v", files)
	}
	if files[0].OldPath != "main.go" {
		t.Errorf("expected OldPath=main.go, got %q", files[0].OldPath)
	}
}

func TestParseDiffOutput_MixedAddDeleteRename(t *testing.T) {
	diff := `diff --git a/b.txt b/b.txt
deleted file mode 100644
index 8616c68..0000000
--- a/b.txt
+++ /dev/null
@@ -1 +0,0 @@
-to-delete
diff --git a/d.txt b/d.txt
new file mode 100644
index 0000000..a1ea743
--- /dev/null
+++ b/d.txt
@@ -0,0 +1 @@
+fresh file
diff --git a/a.txt b/renamed_a.txt
similarity index 100%
rename from a.txt
rename to renamed_a.txt
`
	files := ParseDiffOutput(diff)
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}
	if got := files[0].Status; got != "deleted" {
		t.Errorf("files[0] status=%q want deleted", got)
	}
	if got := files[1].Status; got != "added" {
		t.Errorf("files[1] status=%q want added", got)
	}
	if got := files[2].Status; got != "renamed" {
		t.Errorf("files[2] status=%q want renamed", got)
	}
	if files[2].OldPath != "a.txt" || files[2].Path != "renamed_a.txt" {
		t.Errorf("rename paths wrong: Old=%q New=%q", files[2].OldPath, files[2].Path)
	}
}

func TestParseDiffOutput_CopyDetection(t *testing.T) {
	diff := `diff --git a/src.txt b/dst.txt
similarity index 100%
copy from src.txt
copy to dst.txt
`
	files := ParseDiffOutput(diff)
	if len(files) != 1 || files[0].Status != "copied" {
		t.Fatalf("expected single copied file, got %#v", files)
	}
	if files[0].OldPath != "src.txt" || files[0].Path != "dst.txt" {
		t.Errorf("paths wrong: Old=%q New=%q", files[0].OldPath, files[0].Path)
	}
}

func TestDiffFileDisplayPath_RenameShowsArrow(t *testing.T) {
	f := DiffFile{Status: "renamed", OldPath: "old.txt", Path: "new.txt"}
	if got := diffFileDisplayPath(f); got != "old.txt → new.txt" {
		t.Errorf("expected arrow display, got %q", got)
	}
}

func TestDiffFileDisplayPath_ModifiedShowsOnlyPath(t *testing.T) {
	f := DiffFile{Status: "modified", OldPath: "x.txt", Path: "x.txt"}
	if got := diffFileDisplayPath(f); got != "x.txt" {
		t.Errorf("expected plain path, got %q", got)
	}
}

func TestDiffFileStatusValue_NormalizesUnknownStatus(t *testing.T) {
	if got := diffFileStatusValue(""); got != "modified" {
		t.Errorf("expected empty status to normalize to modified, got %q", got)
	}
	if got := diffFileStatusValue("unexpected"); got != "modified" {
		t.Errorf("expected unknown status to normalize to modified, got %q", got)
	}
	if got := diffFileStatusValue("deleted"); got != "deleted" {
		t.Errorf("expected known status to be preserved, got %q", got)
	}
}

func TestDiffViewer_RenameRendersBadgeAndOldArrowNew(t *testing.T) {
	diff := `diff --git a/old.txt b/new.txt
similarity index 100%
rename from old.txt
rename to new.txt
`
	var buf bytes.Buffer
	err := DiffViewer(diff).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "Renamed") {
		t.Error("expected 'Renamed' status badge in rename diff render")
	}
	if !strings.Contains(body, "old.txt → new.txt") {
		t.Error("expected 'old.txt → new.txt' display path in rename render")
	}
	if !strings.Contains(body, "File renamed from old.txt with no content changes.") {
		t.Error("expected placeholder summary for pure rename")
	}
	if !strings.Contains(body, `data-diff-status="renamed"`) {
		t.Error("expected data-diff-status=renamed on file header")
	}
	// No mis-rendered hunk header in a pure rename.
	if strings.Contains(body, "diff-hunk-header") {
		t.Error("did not expect any diff-hunk-header rows for pure rename body")
	}
}

func TestDiffViewer_DeletedFileRendersDeletedBadge(t *testing.T) {
	diff := `diff --git a/b.txt b/b.txt
deleted file mode 100644
index 8616c68..0000000
--- a/b.txt
+++ /dev/null
@@ -1 +0,0 @@
-to-delete
`
	var buf bytes.Buffer
	err := DiffViewer(diff).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "Deleted") {
		t.Error("expected 'Deleted' status badge for deleted file")
	}
	if !strings.Contains(body, "b.txt") {
		t.Error("expected deleted file name in render")
	}
	if !strings.Contains(body, `data-diff-status="deleted"`) {
		t.Error("expected data-diff-status=deleted")
	}
}

func TestDiffViewer_DeletedBinaryFileShowsPlaceholder(t *testing.T) {
	diff := `diff --git a/img.bin b/img.bin
deleted file mode 100644
index 2f80ba2..0000000
Binary files a/img.bin and /dev/null differ
`
	var buf bytes.Buffer
	err := DiffViewer(diff).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "Deleted") {
		t.Error("expected Deleted badge for binary delete")
	}
	if !strings.Contains(body, "Binary") {
		t.Error("expected Binary badge")
	}
	if !strings.Contains(body, "Binary file deleted.") {
		t.Error("expected binary-file-deleted placeholder text")
	}
}

func TestDiffViewer_AddedFileRendersAddedBadge(t *testing.T) {
	diff := `diff --git a/d.txt b/d.txt
new file mode 100644
index 0000000..a1ea743
--- /dev/null
+++ b/d.txt
@@ -0,0 +1 @@
+fresh file
`
	var buf bytes.Buffer
	err := DiffViewer(diff).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "Added") {
		t.Error("expected Added badge for new file")
	}
}

func TestParseDiffOutput_MissingHunkHeaderStillRendersContent(t *testing.T) {
	diff := `diff --git a/file.txt b/file.txt
+some changes
`
	files := ParseDiffOutput(diff)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if len(files[0].Hunks) != 1 {
		t.Fatalf("expected synthesized hunk, got %d", len(files[0].Hunks))
	}
	if len(files[0].Hunks[0].Lines) < 1 {
		t.Fatalf("expected at least one synthesized diff line, got %d", len(files[0].Hunks[0].Lines))
	}
	if files[0].Hunks[0].Lines[0].Content != "some changes" {
		t.Fatalf("expected content 'some changes', got %q", files[0].Hunks[0].Lines[0].Content)
	}
}

func TestBuildSplitLines(t *testing.T) {
	lines := []DiffLine{
		{Type: "ctx", Content: "line1", OldNum: 1, NewNum: 1},
		{Type: "del", Content: "old line", OldNum: 2, NewNum: 0},
		{Type: "add", Content: "new line", OldNum: 0, NewNum: 2},
		{Type: "ctx", Content: "line3", OldNum: 3, NewNum: 3},
	}

	pairs := buildSplitLines(lines)
	if len(pairs) != 3 {
		t.Fatalf("expected 3 pairs, got %d", len(pairs))
	}

	// First pair: context line
	if pairs[0].Left.Content != "line1" || pairs[0].Right.Content != "line1" {
		t.Errorf("expected context pair, got left=%q right=%q", pairs[0].Left.Content, pairs[0].Right.Content)
	}

	// Second pair: del on left, add on right
	if pairs[1].Left.Type != "del" || pairs[1].Right.Type != "add" {
		t.Errorf("expected del/add pair, got left=%q right=%q", pairs[1].Left.Type, pairs[1].Right.Type)
	}

	// Third pair: context line
	if pairs[2].Left.Content != "line3" || pairs[2].Right.Content != "line3" {
		t.Errorf("expected context pair, got left=%q right=%q", pairs[2].Left.Content, pairs[2].Right.Content)
	}
}

func TestDiffStats(t *testing.T) {
	f := DiffFile{
		Path: "test.go",
		Hunks: []DiffHunk{
			{
				Lines: []DiffLine{
					{Type: "add", Content: "new line 1"},
					{Type: "add", Content: "new line 2"},
					{Type: "del", Content: "old line 1"},
					{Type: "ctx", Content: "unchanged"},
				},
			},
		},
	}
	stats := diffStats(f)
	if stats != "+2 -1" {
		t.Errorf("expected '+2 -1', got %q", stats)
	}
	counts := diffStatCounts(f)
	if counts.Adds != 2 || counts.Dels != 1 {
		t.Errorf("expected split counts +2/-1, got +%d/-%d", counts.Adds, counts.Dels)
	}
	if got := diffStatsAriaLabel(f); got != "2 added lines, 1 deleted lines" {
		t.Errorf("expected accessible stats label, got %q", got)
	}
}

func TestFileCountSuffix(t *testing.T) {
	if fileCountSuffix(1) != "" {
		t.Error("expected empty suffix for 1 file")
	}
	if fileCountSuffix(2) != "s" {
		t.Error("expected 's' suffix for 2 files")
	}
}

func TestShouldDefaultExpand(t *testing.T) {
	tests := []struct {
		name      string
		fileCount int
		want      bool
	}{
		{"0 files expanded", 0, true},
		{"1 file expanded", 1, true},
		{"2 files expanded", 2, true},
		{"3 files expanded", 3, true},
		{"4 files expanded", 4, true},
		{"10 files expanded", 10, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldDefaultExpand(tt.fileCount)
			if got != tt.want {
				t.Errorf("shouldDefaultExpand(%d) = %v, want %v", tt.fileCount, got, tt.want)
			}
		})
	}
}

func TestDiffViewer_EmptyDiff(t *testing.T) {
	var buf bytes.Buffer
	err := DiffViewer("").Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "No changes detected") {
		t.Error("expected 'No changes detected' for empty diff")
	}
}

func TestDiffViewer_FewFiles_DefaultExpanded(t *testing.T) {
	// 2 files => should default expand (chevron has rotate-90)
	diff := `diff --git a/file1.go b/file1.go
--- a/file1.go
+++ b/file1.go
@@ -1,3 +1,4 @@
 package pkg
+import "fmt"
 func A() {
 }
diff --git a/file2.go b/file2.go
--- a/file2.go
+++ b/file2.go
@@ -1,3 +1,3 @@
 package pkg
-func B() {
+func B(x int) {
 }
`
	var buf bytes.Buffer
	err := DiffViewer(diff).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	// Chevron should have rotate-90 class (expanded state)
	if !strings.Contains(body, "diff-chevron-0") {
		t.Error("expected diff-chevron-0 element")
	}
	if !strings.Contains(body, "rotate-90") {
		t.Error("expected rotate-90 class for expanded state with few files")
	}
	// Body should NOT have max-height:0 (expanded)
	if strings.Contains(body, `style="max-height: 0;"`) {
		t.Error("expected no max-height:0 for expanded state with few files")
	}
	// Toggle function should be present
	if !strings.Contains(body, "toggleDiffFile") {
		t.Error("expected toggleDiffFile function in output")
	}
}

func TestDiffViewer_ManyFiles_DefaultExpanded(t *testing.T) {
	// 4 files => should also default expanded (all files expand by default)
	diff := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,1 +1,2 @@
 package a
+// a
diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -1,1 +1,2 @@
 package b
+// b
diff --git a/c.go b/c.go
--- a/c.go
+++ b/c.go
@@ -1,1 +1,2 @@
 package c
+// c
diff --git a/d.go b/d.go
--- a/d.go
+++ b/d.go
@@ -1,1 +1,2 @@
 package d
+// d
`
	var buf bytes.Buffer
	err := DiffViewer(diff).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	// With 4 files, chevrons should have rotate-90 (expanded state)
	if !strings.Contains(body, "diff-chevron-0") {
		t.Error("expected diff-chevron-0 element")
	}
	if !strings.Contains(body, "rotate-90") {
		t.Error("expected rotate-90 class for expanded state")
	}
	// Body should NOT have max-height:0 (expanded)
	if strings.Contains(body, `style="max-height: 0;"`) {
		t.Error("expected no max-height:0 for expanded state")
	}
	// Expand/Collapse All buttons should be present for >1 file
	if !strings.Contains(body, "Expand All") {
		t.Error("expected 'Expand All' button")
	}
	if !strings.Contains(body, "Collapse All") {
		t.Error("expected 'Collapse All' button")
	}
}

func TestDiffViewer_ChevronAndStatsVisible(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }
`
	assertDiffViewerHeaderStats(t, diff, "+1", "-0", "1 added lines, 0 deleted lines")
}

func TestDiffViewer_HeaderStatsUseSemanticColorsForFileStatuses(t *testing.T) {
	tests := []struct {
		name      string
		diff      string
		wantAdd   string
		wantDel   string
		wantLabel string
		wantState string
	}{
		{
			name: "added file",
			diff: `diff --git a/new.txt b/new.txt
new file mode 100644
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+one
+two
`,
			wantAdd:   "+2",
			wantDel:   "-0",
			wantLabel: "2 added lines, 0 deleted lines",
			wantState: "added",
		},
		{
			name: "deleted file",
			diff: `diff --git a/old.txt b/old.txt
deleted file mode 100644
--- a/old.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-one
-two
`,
			wantAdd:   "+0",
			wantDel:   "-2",
			wantLabel: "0 added lines, 2 deleted lines",
			wantState: "deleted",
		},
		{
			name: "modified file",
			diff: `diff --git a/app.go b/app.go
--- a/app.go
+++ b/app.go
@@ -1,2 +1,2 @@
-old
+new
 keep
`,
			wantAdd:   "+1",
			wantDel:   "-1",
			wantLabel: "1 added lines, 1 deleted lines",
			wantState: "modified",
		},
		{
			name: "renamed file",
			diff: `diff --git a/old.go b/new.go
similarity index 80%
rename from old.go
rename to new.go
--- a/old.go
+++ b/new.go
@@ -1 +1 @@
-old
+new
`,
			wantAdd:   "+1",
			wantDel:   "-1",
			wantLabel: "1 added lines, 1 deleted lines",
			wantState: "renamed",
		},
		{
			name: "binary file",
			diff: `diff --git a/img.bin b/img.bin
index 1234567..89abcde 100644
Binary files a/img.bin and b/img.bin differ
`,
			wantAdd:   "+0",
			wantDel:   "-0",
			wantLabel: "0 added lines, 0 deleted lines",
			wantState: "modified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := assertDiffViewerHeaderStats(t, tt.diff, tt.wantAdd, tt.wantDel, tt.wantLabel)
			if !strings.Contains(body, `data-diff-status="`+tt.wantState+`"`) {
				t.Errorf("expected data-diff-status=%s", tt.wantState)
			}
		})
	}
}

func assertDiffViewerHeaderStats(t *testing.T, diff, wantAdd, wantDel, wantLabel string) string {
	t.Helper()
	var buf bytes.Buffer
	err := DiffViewer(diff).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if !strings.Contains(body, "main.go") && strings.Contains(diff, "main.go") {
		t.Error("expected file name 'main.go' to be visible")
	}
	if !strings.Contains(body, `class="diff-file-stats text-xs font-semibold ml-auto shrink-0 flex items-center gap-1"`) {
		t.Error("expected diff stats wrapper to be visible")
	}
	if !strings.Contains(body, `class="diff-stat-add" title="Added lines">`+wantAdd+`</span>`) {
		t.Errorf("expected additions to render as %s in dedicated stats span", wantAdd)
	}
	if !strings.Contains(body, `class="diff-stat-del" title="Deleted lines">`+wantDel+`</span>`) {
		t.Errorf("expected deletions to render as %s in dedicated stats span", wantDel)
	}
	if !strings.Contains(body, `aria-label="`+wantLabel+`"`) {
		t.Errorf("expected accessible diff stats label %q", wantLabel)
	}
	if !strings.Contains(body, `diff-file-stats .diff-stat-add`) || !strings.Contains(body, `color: var(--ov-diff-add-fg);`) {
		t.Error("expected additions to use the muted add foreground color")
	}
	if !strings.Contains(body, `diff-file-stats .diff-stat-del`) || !strings.Contains(body, `color: var(--ov-diff-del-fg);`) {
		t.Error("expected deletions to use the muted delete foreground color")
	}
	if strings.Contains(body, `background-color: var(--ov-diff-add-bg);`) || strings.Contains(body, `background-color: var(--ov-diff-del-bg);`) {
		t.Error("diff header stats should color the numbers, not add chip backgrounds")
	}
	if strings.Contains(body, `color: oklch(var(--su));`) || strings.Contains(body, `color: oklch(var(--er));`) {
		t.Error("diff header stats should not depend on oklch-only DaisyUI colors")
	}

	var baseBuf bytes.Buffer
	if err := layout.Base("Test", nil, "").Render(context.Background(), &baseBuf); err != nil {
		t.Fatalf("base render failed: %v", err)
	}
	baseHTML := baseBuf.String()
	for _, fragment := range []string{
		"--ov-diff-add-bg: #1E3A38;",
		"--ov-diff-del-bg: #3D2C34;",
		"--ov-diff-add-fg: #559B70;",
		"--ov-diff-del-fg: #BD7076;",
		"--ov-diff-add-bg: #DDEDE0;",
		"--ov-diff-del-bg: #FAE3E1;",
		"--ov-diff-add-fg: #317A4A;",
		"--ov-diff-del-fg: #A65353;",
		`[data-theme="dark"] .diff-file-card .bg-success\/10 {`,
		"background-color: var(--ov-diff-add-bg) !important;",
		`[data-theme="dark"] .diff-file-card .bg-error\/10 {`,
		"background-color: var(--ov-diff-del-bg) !important;",
		`[data-theme="light"] .diff-file-card .bg-success\/10 {`,
		`[data-theme="light"] .diff-file-card .bg-error\/10 {`,
	} {
		if !strings.Contains(baseHTML, fragment) {
			t.Errorf("expected desktop-safe diff color fragment %q", fragment)
		}
	}

	if !strings.Contains(body, "M9 5l7 7-7 7") {
		t.Error("expected chevron SVG path")
	}
	if !strings.Contains(body, "data-diff-toggle") {
		t.Error("expected data-diff-toggle attribute on header")
	}
	return body
}

func TestDiffViewer_FileHeaderRendersCopyPathButton(t *testing.T) {
	longPath := "internal/very/deep/package/with/a/really/long/path/name/that/should/truncate/without/hiding/copy/button/example_handler.go"
	diff := fmt.Sprintf(`diff --git a/%s b/%s
--- a/%s
+++ b/%s
@@ -1 +1 @@
-old
+new
`, longPath, longPath, longPath, longPath)

	var buf bytes.Buffer
	if err := DiffViewer(diff).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if !strings.Contains(body, `class="diff-copy-path-btn`) {
		t.Fatal("expected file header copy path button")
	}
	if !strings.Contains(body, `data-copy-path="`+longPath+`"`) {
		t.Fatalf("expected copy button to carry long file path %q", longPath)
	}
	if !strings.Contains(body, `<span class="flex items-center gap-1 min-w-0 flex-1"><span class="font-mono text-sm font-medium truncate min-w-0"`) {
		t.Fatal("expected copy button to render in the filename group")
	}
	if !strings.Contains(body, `onclick="copyDiffFilePath(event, this)"`) {
		t.Fatal("expected copy button to use the stop-propagating copy handler")
	}
	if !strings.Contains(body, `function copyDiffFilePath(ev, button)`) || !strings.Contains(body, `ev.stopPropagation()`) {
		t.Fatal("expected copy handler to prevent header collapse toggle")
	}
	if strings.Contains(body, `showToast('File path copied', 'completed')`) {
		t.Fatal("expected no successful copy toast feedback")
	}
	if !strings.Contains(body, `showToast('Failed to copy file path', 'failed')`) {
		t.Fatal("expected failed copy toast feedback")
	}
}

func TestDiffViewer_FileHeaderCopyButtonUsesRenamedDisplayPath(t *testing.T) {
	diff := `diff --git a/old/name.go b/new/name.go
similarity index 92%
rename from old/name.go
rename to new/name.go
--- a/old/name.go
+++ b/new/name.go
@@ -1 +1 @@
-old
+new
`

	var buf bytes.Buffer
	if err := DiffViewer(diff).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if !strings.Contains(body, `data-copy-path="old/name.go → new/name.go"`) {
		t.Fatal("expected renamed file copy button to carry the displayed old-to-new path")
	}
}

func TestBuildCommentMap(t *testing.T) {
	comments := []models.ReviewComment{
		{FilePath: "main.go", LineNumber: 10, LineType: "new", CommentText: "Comment A"},
		{FilePath: "main.go", LineNumber: 10, LineType: "new", CommentText: "Comment B"},
		{FilePath: "handler.go", LineNumber: 5, LineType: "old", CommentText: "Comment C"},
	}

	m := buildCommentMap(comments)
	if len(m) != 2 {
		t.Errorf("expected 2 keys in map, got %d", len(m))
	}

	key1 := commentKey("main.go", 10, "new")
	if len(m[key1]) != 2 {
		t.Errorf("expected 2 comments for main.go:10:new, got %d", len(m[key1]))
	}

	key2 := commentKey("handler.go", 5, "old")
	if len(m[key2]) != 1 {
		t.Errorf("expected 1 comment for handler.go:5:old, got %d", len(m[key2]))
	}
}

func TestLineHasComment(t *testing.T) {
	comments := []models.ReviewComment{
		{FilePath: "main.go", LineNumber: 10, LineType: "new", CommentText: "test"},
	}
	m := buildCommentMap(comments)

	// Line with comment
	addLine := DiffLine{Type: "add", NewNum: 10}
	if !lineHasComment(m, "main.go", addLine) {
		t.Error("expected lineHasComment=true for add line with NewNum=10")
	}

	// Line without comment
	noCommentLine := DiffLine{Type: "add", NewNum: 20}
	if lineHasComment(m, "main.go", noCommentLine) {
		t.Error("expected lineHasComment=false for line without comment")
	}

	// Del line checks OldNum
	delComments := []models.ReviewComment{
		{FilePath: "main.go", LineNumber: 5, LineType: "old", CommentText: "test"},
	}
	dm := buildCommentMap(delComments)
	delLine := DiffLine{Type: "del", OldNum: 5}
	if !lineHasComment(dm, "main.go", delLine) {
		t.Error("expected lineHasComment=true for del line with OldNum=5")
	}
}

func TestLineTypeForDiff(t *testing.T) {
	tests := []struct {
		diffType string
		want     string
	}{
		{"add", "new"},
		{"del", "old"},
		{"ctx", "ctx"},
	}
	for _, tt := range tests {
		got := lineTypeForDiff(tt.diffType)
		if got != tt.want {
			t.Errorf("lineTypeForDiff(%q) = %q, want %q", tt.diffType, got, tt.want)
		}
	}
}

func TestLineNumForReview(t *testing.T) {
	addLine := DiffLine{Type: "add", NewNum: 42, OldNum: 0}
	if lineNumForReview(addLine) != 42 {
		t.Errorf("expected 42 for add line, got %d", lineNumForReview(addLine))
	}

	delLine := DiffLine{Type: "del", NewNum: 0, OldNum: 10}
	if lineNumForReview(delLine) != 10 {
		t.Errorf("expected 10 for del line, got %d", lineNumForReview(delLine))
	}

	ctxLine := DiffLine{Type: "ctx", NewNum: 5, OldNum: 5}
	if lineNumForReview(ctxLine) != 5 {
		t.Errorf("expected 5 for ctx line, got %d", lineNumForReview(ctxLine))
	}
}

func TestDiffViewerWithReview_EmptyDiff(t *testing.T) {
	var buf bytes.Buffer
	err := DiffViewerWithReview("", "task123", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "No changes detected") {
		t.Error("expected 'No changes detected' for empty diff")
	}
}

func TestDiffViewerWithReview_WithComments(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }
`
	comments := []models.ReviewComment{
		{ID: "c1", FilePath: "main.go", LineNumber: 2, LineType: "new", CommentText: "Why import fmt?", ReviewedBy: "user"},
	}

	var buf bytes.Buffer
	err := DiffViewerWithReview(diff, "task123", comments).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	// Should contain the review toolbar
	if !strings.Contains(body, "review-toolbar") {
		t.Error("expected review-toolbar")
	}
	// Should contain the comment text
	if !strings.Contains(body, "Why import fmt?") {
		t.Error("expected comment text in rendered output")
	}
	// Review count badges should be hidden from the Changes UI
	if strings.Contains(body, "1 comment") {
		t.Error("did not expect review comment count badge text")
	}
	if strings.Contains(body, "badge badge-warning badge-sm gap-1") {
		t.Error("did not expect review comment count badge markup")
	}
	// Should contain submit review button
	if !strings.Contains(body, "Submit Review") {
		t.Error("expected Submit Review button")
	}
	// Should contain inline comment JavaScript
	if !strings.Contains(body, "openInlineCommentForm") {
		t.Error("expected openInlineCommentForm function")
	}
	// Should contain diff-review-line class for commentable lines
	if !strings.Contains(body, "diff-review-line") {
		t.Error("expected diff-review-line class for interactive lines")
	}
	// Should contain data attributes for review
	if !strings.Contains(body, `data-task-id="task123"`) {
		t.Error("expected data-task-id attribute")
	}
}

func TestDiffViewerWithReview_ServerRenderedCommentUsesConsistentDeleteButtonStyle(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }
`
	comments := []models.ReviewComment{
		{ID: "c1", FilePath: "main.go", LineNumber: 2, LineType: "new", CommentText: "Why import fmt?", ReviewedBy: "user"},
	}

	var buf bytes.Buffer
	err := DiffViewerWithReview(diff, "task123", comments).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if !strings.Contains(body, `class="btn btn-circle btn-ghost btn-xs review-delete-btn"`) {
		t.Error("expected server-rendered inline comments to use circular delete button classes")
	}
	if strings.Contains(body, `review-delete-btn" onclick="deleteReviewComment(event)" aria-label="Delete review comment">&times;</button>`) {
		t.Error("expected server-rendered inline comments to avoid legacy small × delete button markup")
	}
}

func TestDiffViewerWithReview_NoComments_ShowsAddCommentButtons(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }
`
	var buf bytes.Buffer
	err := DiffViewerWithReview(diff, "task123", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	// Should NOT have the old Start Review button
	if strings.Contains(body, "Start Review") {
		t.Error("should not show 'Start Review' button — inline commenting is always active")
	}
	// Should have inline '+' add-comment buttons on diff lines
	if !strings.Contains(body, "diff-add-comment-btn") {
		t.Error("expected '+' add-comment buttons on diff lines")
	}
	if !strings.Contains(body, "review-comment-shell") {
		t.Error("expected inline review comment shell styling")
	}
	if !strings.Contains(body, "review-comment-shell rounded-lg border border-base-300 bg-base-100 p-3") {
		t.Error("expected inline review shell with single merged border")
	}
	if !strings.Contains(body, "requestAnimationFrame(function()") {
		t.Error("expected add-comment flow to autofocus after form row is inserted")
	}
	if !strings.Contains(body, "review-comment-actions mt-3 flex items-center justify-end gap-2") {
		t.Error("expected inline review buttons below the textarea but visually inside the shell")
	}
	if !strings.Contains(body, "rows=\"2\"") {
		t.Error("expected shorter inline review textarea")
	}
	if !strings.Contains(body, "min-height: 56px;") {
		t.Error("expected shorter inline review textarea minimum height")
	}
	if !strings.Contains(body, "review-comment-textarea block w-full") {
		t.Error("expected inline review comment textarea styling")
	}
	if !strings.Contains(body, "padding: 0;") {
		t.Error("expected textarea to use full width without right padding for buttons")
	}
	if strings.Count(body, "<td class=\"diff-line-num px-2 py-0 w-12 border-r border-base-300 bg-base-200\"></td>") < 2 {
		t.Error("expected inline review form row to keep both line-number gutter cells")
	}
	if !strings.Contains(body, "<td class=\"p-2 bg-base-200\">") {
		t.Error("expected inline review form row to render the form only in the code column")
	}
	// Submit Review stays in the toolbar but is disabled until comments exist
	if !strings.Contains(body, "Submit Review") {
		t.Error("expected 'Submit Review' button in toolbar")
	}
	if !strings.Contains(body, "disabled") {
		t.Error("expected 'Submit Review' to be disabled when no comments")
	}
}

func TestDiffViewerWithReview_WithComments_ShowsSubmitAndAddButtons(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }
`
	comments := []models.ReviewComment{
		{ID: "c1", FilePath: "main.go", LineNumber: 2, LineType: "new", CommentText: "Why?", ReviewedBy: "user"},
	}

	var buf bytes.Buffer
	err := DiffViewerWithReview(diff, "task123", comments).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	// Submit Review should appear when there are comments
	if !strings.Contains(body, "Submit Review") {
		t.Error("expected 'Submit Review' button when comments exist")
	}
	// The '+' add-comment buttons should still be present for adding more comments
	if !strings.Contains(body, "diff-add-comment-btn") {
		t.Error("expected '+' add-comment buttons even when comments exist")
	}
	// Should NOT have Start Review
	if strings.Contains(body, "Start Review") {
		t.Error("should not show 'Start Review' button")
	}
}

func TestDiffViewerWithReview_SplitViewHasAddCommentButtons(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }
`

	var buf bytes.Buffer
	err := DiffViewerWithReview(diff, "task123", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if !strings.Contains(body, `id="diff-content-split"`) {
		t.Fatal("expected split diff content container")
	}
	if !strings.Contains(body, `data-review-layout="split"`) {
		t.Error("expected split view rows/buttons to be review-enabled")
	}
	if !strings.Contains(body, `data-review-side="right"`) {
		t.Error("expected split view add-comment button on the right side")
	}
	if !strings.Contains(body, `if ((options.layout || 'inline') === 'split')`) {
		t.Error("expected split layout branch when opening review form")
	}
	if !strings.Contains(body, `formRow.innerHTML = '<td class="diff-line-num px-2 py-0 border-r border-base-300 bg-base-200"></td>' +`) {
		t.Error("expected split review form row structure")
	}
	if !strings.Contains(body, `'<td class="p-0 bg-warning/15 align-top">' +`) {
		t.Error("expected split review comment box in the final column with full yellow background")
	}
	if !strings.Contains(body, `submitLabel: 'Update'`) {
		t.Error("expected split view yellow comment row to reopen as the review form for editing")
	}
}

func TestDiffViewerWithReview_NoComments_RendersReviewCommentsContainer(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }
`

	var buf bytes.Buffer
	err := DiffViewerWithReview(diff, "task123", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if strings.Contains(body, `id="review-comments-list"`) {
		t.Error("should not render separate review comments summary list; comments should be inline on code lines")
	}
}

func TestDiffViewerWithReview_AddCommentAutofocusAfterInsert(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }
`

	var buf bytes.Buffer
	err := DiffViewerWithReview(diff, "task123", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if !strings.Contains(body, `requestAnimationFrame(function()`) {
		t.Error("expected add-comment click flow to focus after row insertion")
	}
	if !strings.Contains(body, `var textarea = formRow.querySelector('.review-comment-textarea');`) {
		t.Error("expected add-comment flow to target the inserted review textarea")
	}
	if !strings.Contains(body, `textarea.focus();`) {
		t.Error("expected add-comment flow to focus the inserted textarea")
	}
}

func TestDiffViewerWithReview_EnterSubmitsInlineComment(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }
`

	var buf bytes.Buffer
	err := DiffViewerWithReview(diff, "task123", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if !strings.Contains(body, `e.key === 'Enter' && !e.shiftKey && !e.isComposing`) {
		t.Error("expected Enter to submit inline comment while Shift+Enter keeps newline")
	}
	if !strings.Contains(body, `e.preventDefault();`) {
		t.Error("expected Enter handler to prevent newline before submitting")
	}
}

func TestDiffViewerWithReview_SubmitCommentReplacesFormInline(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }
`

	var buf bytes.Buffer
	err := DiffViewerWithReview(diff, "task123", nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if !strings.Contains(body, `if (formRow && formRow.parentNode)`) {
		t.Error("expected submit flow to replace inline form in-place without page refresh")
	}
	if !strings.Contains(body, `formRow.replaceWith(buildInlineCommentRow(`) {
		t.Error("expected comment submit to render yellow inline comment row immediately")
	}
	if !strings.Contains(body, `refreshReviewComments(taskId)`) {
		t.Error("expected submit flow to refresh/sync comments across inline and split views")
	}
	if !strings.Contains(body, `syncReviewCommentsFromListHtml`) {
		t.Error("expected shared comment sync function for inline/split consistency")
	}
	if !strings.Contains(body, `catch(function(err)`) {
		t.Error("expected submit flow to handle request failures")
	}
}

func TestDiffViewerWithReview_InlineCommentRowsSupportEditAndDelete(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }
`

	comments := []models.ReviewComment{
		{ID: "c1", FilePath: "main.go", LineNumber: 2, LineType: "new", CommentText: "Why?", ReviewedBy: "user"},
	}

	var buf bytes.Buffer
	err := DiffViewerWithReview(diff, "task123", comments).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if !strings.Contains(body, `class="review-inline-comment"`) {
		t.Error("expected inline rendered review comment rows")
	}
	if !strings.Contains(body, `onclick="startEditReviewComment(event)"`) {
		t.Error("expected clicking yellow comment area to start edit mode")
	}
	if !strings.Contains(body, `submitLabel: 'Update'`) {
		t.Error("expected clicking yellow comment area to switch back to the review form in update mode")
	}
	if !strings.Contains(body, `formRow.replaceWith(buildInlineCommentRow({`) || !strings.Contains(body, `commentText: formRow.dataset.originalText || ''`) {
		t.Error("expected canceling edit mode to restore the original yellow review comment without refresh")
	}
	if !strings.Contains(body, `method: 'PATCH'`) {
		t.Error("expected PATCH call for editing existing review comments")
	}
	if !strings.Contains(body, `review-delete-btn`) || !strings.Contains(body, `btn-circle btn-ghost btn-xs`) {
		t.Error("expected delete button to use the task card close button style")
	}
	if !strings.Contains(body, `h-4 w-4`) {
		t.Error("expected larger white delete icon")
	}
	if !strings.Contains(body, `review-inline-comment-bar`) {
		t.Error("expected solid yellow comment bar on left edge")
	}
	if !strings.Contains(body, `bg-warning/15 align-top`) {
		t.Error("expected full yellow comment background in code column")
	}
	if !strings.Contains(body, `deleteReviewComment`) {
		t.Error("expected JS delete handler for inline review comments")
	}
	if strings.Contains(body, `comment.ReviewedBy`) || strings.Contains(body, `comment.FilePath`) {
		t.Error("should not show user or line metadata inside yellow comment box")
	}
}

func TestBuildDiffRenderMetas_AutoLoadVsDeferred(t *testing.T) {
	autoFile := DiffFile{
		Path: "small.txt",
		Hunks: []DiffHunk{
			{Header: "@@ -1,1 +1,2 @@", Lines: make([]DiffLine, autoLoadFileDiffLines-10)},
		},
	}
	deferredFile := DiffFile{
		Path: "large.txt",
		Hunks: []DiffHunk{
			{Header: "@@ -1,1 +1,2 @@", Lines: make([]DiffLine, autoLoadFileDiffLines+50)},
		},
	}

	metas := buildDiffRenderMetas([]DiffFile{autoFile, deferredFile})
	if len(metas) != 2 {
		t.Fatalf("expected 2 metas, got %d", len(metas))
	}
	if !metas[0].AutoLoad {
		t.Fatal("expected first file to auto-load")
	}
	if !metas[1].CanLoadOnDemand {
		t.Fatal("expected second file to be deferred and loadable on demand")
	}
}

func TestBuildDiffRenderMetas_SingleFileHardLimitBlocksLoad(t *testing.T) {
	tooBig := DiffFile{
		Path: "huge.txt",
		Hunks: []DiffHunk{
			{Header: "@@ -1,1 +1,2 @@", Lines: make([]DiffLine, maxLoadableFileDiffLines+10)},
		},
	}

	metas := buildDiffRenderMetas([]DiffFile{tooBig})
	if len(metas) != 1 {
		t.Fatalf("expected 1 meta, got %d", len(metas))
	}
	if metas[0].AutoLoad || metas[0].CanLoadOnDemand {
		t.Fatal("expected oversized single file to be blocked")
	}
	if !strings.Contains(metas[0].BlockedReason, "single-file limit") {
		t.Fatal("expected blocked reason to mention single-file limit")
	}
}

func TestBuildDiffRenderMetas_TotalLimitBlocksLaterFiles(t *testing.T) {
	first := DiffFile{
		Path: "first.txt",
		Hunks: []DiffHunk{
			{Header: "@@ -1,1 +1,2 @@", Lines: make([]DiffLine, 15000)},
		},
	}
	second := DiffFile{
		Path: "second.txt",
		Hunks: []DiffHunk{
			{Header: "@@ -1,1 +1,2 @@", Lines: make([]DiffLine, 7000)},
		},
	}

	metas := buildDiffRenderMetas([]DiffFile{first, second})
	if len(metas) != 2 {
		t.Fatalf("expected 2 metas, got %d", len(metas))
	}
	if metas[1].CanLoadOnDemand || metas[1].AutoLoad {
		t.Fatal("expected second file to be blocked by total limit")
	}
	if !strings.Contains(metas[1].BlockedReason, "Total diff load limit") {
		t.Fatal("expected blocked reason to mention total diff load limit")
	}
}

func TestBuildDiffRenderMetas_MaxFiles300(t *testing.T) {
	files := make([]DiffFile, maxDiffFiles+5)
	for i := range files {
		files[i] = DiffFile{
			Path: fmt.Sprintf("f%d.txt", i),
			Hunks: []DiffHunk{
				{Header: "@@ -1,1 +1,1 @@", Lines: []DiffLine{{Type: "add", Content: "x"}}},
			},
		}
	}
	metas := buildDiffRenderMetas(files)
	if len(metas) != maxDiffFiles {
		t.Fatalf("expected %d metas, got %d", maxDiffFiles, len(metas))
	}
}

func TestReviewTargetForDiffLineSharedLookupMetadata(t *testing.T) {
	commentMap := buildCommentMap([]models.ReviewComment{
		{ID: "new-comment", FilePath: "main.go", LineNumber: 4, LineType: "new", CommentText: "new line"},
		{ID: "old-comment", FilePath: "main.go", LineNumber: 2, LineType: "old", CommentText: "old line"},
		{ID: "ctx-comment", FilePath: "main.go", LineNumber: 1, LineType: "ctx", CommentText: "context line"},
	})

	tests := []struct {
		name       string
		line       DiffLine
		lineNumber int
		lineType   string
		commentID  string
		canComment bool
	}{
		{name: "added", line: DiffLine{Type: "add", NewNum: 4}, lineNumber: 4, lineType: "new", commentID: "new-comment", canComment: true},
		{name: "deleted", line: DiffLine{Type: "del", OldNum: 2}, lineNumber: 2, lineType: "old", commentID: "old-comment", canComment: true},
		{name: "context", line: DiffLine{Type: "ctx", OldNum: 1, NewNum: 1}, lineNumber: 1, lineType: "ctx", commentID: "ctx-comment", canComment: true},
		{name: "empty", line: DiffLine{Type: "empty"}, lineNumber: 0, lineType: "new", canComment: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := reviewTargetForDiffLine(tt.line)
			if target.LineNumber != tt.lineNumber || target.LineType != tt.lineType || target.CanComment != tt.canComment {
				t.Fatalf("target = %+v, want line=%d type=%q canComment=%v", target, tt.lineNumber, tt.lineType, tt.canComment)
			}
			if got := lineNumForReview(tt.line); got != tt.lineNumber {
				t.Fatalf("lineNumForReview = %d, want %d", got, tt.lineNumber)
			}
			if got := lineTypeForDiff(tt.line.Type); got != tt.lineType {
				t.Fatalf("lineTypeForDiff = %q, want %q", got, tt.lineType)
			}

			comments := getLineComments(commentMap, "main.go", tt.line)
			if !tt.canComment {
				if lineHasComment(commentMap, "main.go", tt.line) || comments != nil {
					t.Fatal("non-commentable line should not resolve comments")
				}
				return
			}
			if !lineHasComment(commentMap, "main.go", tt.line) {
				t.Fatal("expected shared lookup to find comment")
			}
			if len(comments) != 1 || comments[0].ID != tt.commentID {
				t.Fatalf("comments = %+v, want %s", comments, tt.commentID)
			}
		})
	}
}

func TestDiffViewerWithReview_RendersSharedReviewCommentRows(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
-func old() {}
+func new() {}
+func added() {}
 var x = 1
`
	comments := []models.ReviewComment{
		{ID: "add-comment", TaskID: "task123", FilePath: "main.go", LineNumber: 3, LineType: "new", CommentText: "added line note"},
		{ID: "ctx-comment", TaskID: "task123", FilePath: "main.go", LineNumber: 1, LineType: "ctx", CommentText: "context line note"},
		{ID: "old-comment", TaskID: "task123", FilePath: "main.go", LineNumber: 2, LineType: "old", CommentText: "deleted line note"},
	}

	var buf bytes.Buffer
	if err := DiffViewerWithReviewView(diff, "task123", comments, "inline").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	for _, fragment := range []string{
		`class="review-inline-comment" data-comment-id="add-comment" data-file-path="main.go" data-line-num="3" data-line-type="new" data-review-layout="inline"`,
		`class="review-inline-comment" data-comment-id="add-comment" data-file-path="main.go" data-line-num="3" data-line-type="new" data-review-layout="split"`,
		`class="review-inline-comment" data-comment-id="ctx-comment" data-file-path="main.go" data-line-num="1" data-line-type="ctx" data-review-layout="inline"`,
		`class="review-inline-comment" data-comment-id="ctx-comment" data-file-path="main.go" data-line-num="1" data-line-type="ctx" data-review-layout="split"`,
		`class="review-inline-comment" data-comment-id="old-comment" data-file-path="main.go" data-line-num="2" data-line-type="old" data-review-layout="inline"`,
		`class="review-inline-comment-box p-2 pl-3 pr-8 text-xs cursor-text" data-comment-id="add-comment" onclick="startEditReviewComment(event)"`,
		`class="btn btn-circle btn-ghost btn-xs review-delete-btn" onclick="deleteReviewComment(event)" aria-label="Delete review comment"`,
		`class="review-comment-text whitespace-pre-wrap break-words">added line note</p>`,
		`<td class="diff-line-num px-2 py-0 w-12 border-r border-base-300 bg-base-200"></td>`,
		`<td class="diff-line-content px-3 py-0 border-r border-base-300 bg-base-200"></td>`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("expected rendered review comment fragment %q", fragment)
		}
	}
	if strings.Count(body, `data-comment-id="add-comment"`) < 4 {
		t.Fatalf("expected add comment row and editable box in both layouts, body contained %d add-comment ids", strings.Count(body, `data-comment-id="add-comment"`))
	}
	if strings.Count(body, `class="diff-add-comment-btn"`) < 2 {
		t.Fatalf("expected add-comment buttons in both inline and split layouts")
	}
	if !strings.Contains(body, `Submit Review`) || !strings.Contains(body, `id="review-toolbar"`) {
		t.Fatal("expected submit-review toolbar state to remain rendered")
	}
}

func TestDiffViewerWithReview_LargeFileRendersLoadDiffButton(t *testing.T) {
	diff := "diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -0,0 +1,1000 @@\n" + strings.Repeat("+line\n", autoLoadFileDiffLines+20)

	var buf bytes.Buffer
	if err := DiffViewerWithReview(diff, "task123", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if !strings.Contains(body, "Large file diff not shown by default") {
		t.Error("expected deferred large-file message")
	}
	if !strings.Contains(body, "Load diff") {
		t.Error("expected load diff button")
	}
	if !strings.Contains(body, `data-copy-path="big.txt"`) {
		t.Error("expected deferred large-file header copy path button")
	}
	if !strings.Contains(body, `<span class="flex items-center gap-1 min-w-0 flex-1"><span class="font-mono text-sm font-medium truncate min-w-0" title="big.txt">big.txt</span><button`) {
		t.Error("expected deferred large-file copy button next to filename")
	}
	if !strings.Contains(body, `onclick="toggleDiffFile(0)"`) {
		t.Error("expected deferred large-file header to toggle collapse")
	}
	if !strings.Contains(body, `data-diff-toggle="0"`) {
		t.Error("expected deferred large-file header to participate in persisted collapse state")
	}
	if !strings.Contains(body, `id="diff-chevron-0"`) || !strings.Contains(body, `id="diff-chevron-split-0"`) {
		t.Error("expected deferred large-file cards to render inline and split chevrons")
	}
	if !strings.Contains(body, `id="diff-body-0"`) || !strings.Contains(body, `id="diff-body-split-0"`) {
		t.Error("expected deferred large-file placeholders to expose collapsible body targets")
	}
	if !strings.Contains(body, `class="diff-file-body overflow-hidden transition-all duration-300 ease-in-out"`) {
		t.Error("expected deferred placeholder body to use normal collapsible body styling")
	}
	if strings.Contains(body, `class="diff-file-body overflow-hidden transition-all duration-300 ease-in-out p-6`) {
		t.Error("deferred placeholder body should not keep padding on the collapsed element")
	}
	if !strings.Contains(body, `class="p-6 bg-base-100 text-center"`) {
		t.Error("expected deferred placeholder content wrapper to keep placeholder spacing")
	}
	if strings.Contains(body, `<table class="diff-table`) {
		t.Error("deferred large-file placeholder should not mount heavy diff table DOM")
	}
	if !strings.Contains(body, `/tasks/task123/changes/file?file_index=0&amp;view=inline&amp;review=true`) {
		t.Error("expected inline load-diff endpoint")
	}
	if !strings.Contains(body, `/tasks/task123/changes/file?file_index=0&amp;view=split&amp;review=true`) {
		t.Error("expected split load-diff endpoint")
	}
}

func TestLoadDiffFileCard_RendersRequestedView(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,1 +1,2 @@
-package main
+package main
+import "fmt"
`
	var buf bytes.Buffer
	if err := LoadDiffFileCard(diff, 0, "split", "task123", nil, true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, `id="diff-file-split-0"`) {
		t.Error("expected split card to render for requested file")
	}
}

func TestLoadDiffFileCardMeta_MatchesFullParseForReviewInlineAndSplit(t *testing.T) {
	diff := `diff --git a/ignored.txt b/ignored.txt
--- a/ignored.txt
+++ b/ignored.txt
@@ -1 +1 @@
-old
+new
diff --git a/src/review.go b/src/review.go
--- a/src/review.go
+++ b/src/review.go
@@ -7,4 +7,4 @@ func example() {
 context line
-removed only
-old value
+new value
 unchanged
+added tail
`
	fullMeta, ok := getDiffRenderMetaByIndex(ParseDiffOutput(diff), 1)
	if !ok {
		t.Fatal("expected full parse metadata")
	}
	targetedMeta, ok := DiffRenderMetaByIndex(diff, 1)
	if !ok {
		t.Fatal("expected targeted metadata")
	}
	if fullMeta.File.Path != targetedMeta.File.Path || fullMeta.File.Status != targetedMeta.File.Status || fullMeta.LineCount != targetedMeta.LineCount || fullMeta.CharCount != targetedMeta.CharCount {
		t.Fatalf("targeted metadata differs from full parse: full=%#v targeted=%#v", fullMeta, targetedMeta)
	}
	if len(fullMeta.File.Hunks) != len(targetedMeta.File.Hunks) || len(fullMeta.File.Hunks[0].Lines) != len(targetedMeta.File.Hunks[0].Lines) {
		t.Fatalf("targeted hunks differ from full parse: full=%#v targeted=%#v", fullMeta.File.Hunks, targetedMeta.File.Hunks)
	}
	for i, line := range fullMeta.File.Hunks[0].Lines {
		got := targetedMeta.File.Hunks[0].Lines[i]
		if line != got {
			t.Fatalf("line %d differs: full=%#v targeted=%#v", i, line, got)
		}
	}

	comments := []models.ReviewComment{
		{ID: "old-comment", FilePath: "src/review.go", LineNumber: 9, LineType: "old", CommentText: "old line review"},
		{ID: "new-comment", FilePath: "src/review.go", LineNumber: 8, LineType: "new", CommentText: "new line review"},
		{ID: "ctx-comment", FilePath: "src/review.go", LineNumber: 9, LineType: "ctx", CommentText: "context review"},
	}
	for _, view := range []string{"inline", "split"} {
		var buf bytes.Buffer
		if err := LoadDiffFileCardMeta(targetedMeta, true, view, "task123", comments, true).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render %s failed: %v", view, err)
		}
		body := buf.String()
		wants := []string{"src/review.go", `data-diff-status="modified"`, `@@ -7,4 +7,4 @@ func example()`, "old value", "new value", "added tail", "new line review", "context review", `data-line-num="8"`, `data-line-type="new"`, `data-line-type="ctx"`}
		if view == "inline" {
			wants = append(wants, "old line review", `data-line-type="old"`)
		}
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Fatalf("%s render missing %q:\n%s", view, want, body)
			}
		}
	}
}

func TestDiffRenderMetaByIndex_PreservesSpecialStatusesAndLimits(t *testing.T) {
	tooLarge := strings.Repeat("+oversized line\n", maxLoadableFileDiffLines+1)
	totalLimitFillerLines := maxLoadableTotalDiffLines - 2
	totalLimitFiller := strings.Repeat("+filler line\n", totalLimitFillerLines)
	diff := `diff --git a/untracked.txt b/untracked.txt
new file mode 100644
--- /dev/null
+++ b/untracked.txt
@@ -0,0 +1,1 @@
+untracked
diff --git a/old-name.txt b/new-name.txt
similarity index 100%
rename from old-name.txt
rename to new-name.txt
diff --git a/deleted.txt b/deleted.txt
deleted file mode 100644
--- a/deleted.txt
+++ /dev/null
@@ -1 +0,0 @@
-gone
diff --git a/image.bin b/image.bin
new file mode 100644
Binary files /dev/null and b/image.bin differ
diff --git a/huge.txt b/huge.txt
--- a/huge.txt
+++ b/huge.txt
@@ -1 +1,` + fmt.Sprintf("%d", maxLoadableFileDiffLines+1) + ` @@
` + tooLarge + `diff --git a/filler.txt b/filler.txt
--- a/filler.txt
+++ b/filler.txt
@@ -1 +1,` + fmt.Sprintf("%d", totalLimitFillerLines) + ` @@
` + totalLimitFiller + `diff --git a/after-total.txt b/after-total.txt
--- a/after-total.txt
+++ b/after-total.txt
@@ -1 +1,2 @@
 keep
+blocked by total
`

	cases := []struct {
		index   int
		path    string
		status  string
		binary  bool
		blocked string
	}{
		{index: 0, path: "untracked.txt", status: "added"},
		{index: 1, path: "new-name.txt", status: "renamed"},
		{index: 2, path: "deleted.txt", status: "deleted"},
		{index: 3, path: "image.bin", status: "added", binary: true},
		{index: 4, path: "huge.txt", status: "modified", blocked: "single-file limit"},
		{index: 6, path: "after-total.txt", status: "modified", blocked: "Total diff load limit"},
	}
	for _, tc := range cases {
		meta, ok := DiffRenderMetaByIndex(diff, tc.index)
		if !ok {
			t.Fatalf("expected meta for index %d", tc.index)
		}
		if meta.File.Path != tc.path || meta.File.Status != tc.status || meta.File.IsBinary != tc.binary {
			t.Fatalf("index %d metadata mismatch: %#v", tc.index, meta.File)
		}
		if tc.blocked != "" && !strings.Contains(meta.BlockedReason, tc.blocked) {
			t.Fatalf("index %d expected blocked reason containing %q, got %q", tc.index, tc.blocked, meta.BlockedReason)
		}
	}
}

func TestDiffRenderMetaByIndex_LargeDiffProbeReducesParsedBytesAndAllocations(t *testing.T) {
	diff := buildMixedLargeDiff(200)
	const targetIndex = 9
	legacyParsedBytes := len(diff) * 4
	_, optimizedParsedBytes, ok := diffRenderMetaByIndexWithParsedBytes(diff, targetIndex)
	if !ok {
		t.Fatal("expected targeted metadata")
	}
	if optimizedParsedBytes > legacyParsedBytes/10 {
		t.Fatalf("expected at least 90%% raw parsed-byte reduction, legacy=%d optimized=%d", legacyParsedBytes, optimizedParsedBytes)
	}

	legacyAllocs := testing.AllocsPerRun(20, func() {
		legacyRepeatedParseMeta(diff, targetIndex)
	})
	optimizedAllocs := testing.AllocsPerRun(20, func() {
		DiffRenderMetaByIndex(diff, targetIndex)
	})
	if optimizedAllocs > legacyAllocs*0.25 {
		t.Fatalf("expected at least 75%% allocation reduction, legacy=%.0f optimized=%.0f", legacyAllocs, optimizedAllocs)
	}
}

func BenchmarkDiffRenderMetaByIndexLargeDiff(b *testing.B) {
	diff := buildMixedLargeDiff(200)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, ok := DiffRenderMetaByIndex(diff, 9); !ok {
			b.Fatal("missing target file")
		}
	}
}

func legacyRepeatedParseMeta(diff string, fileIndex int) DiffFileRenderMeta {
	if _, ok := getDiffRenderMetaByIndex(ParseDiffOutput(diff), fileIndex); !ok {
		return DiffFileRenderMeta{}
	}
	meta, _ := getDiffRenderMetaByIndex(ParseDiffOutput(diff), fileIndex)
	if !(meta.AutoLoad || meta.CanLoadOnDemand) {
		return meta
	}
	getDiffRenderMetaByIndex(ParseDiffOutput(diff), fileIndex)
	meta, _ = getDiffRenderMetaByIndex(ParseDiffOutput(diff), fileIndex)
	return meta
}

func buildMixedLargeDiff(fileCount int) string {
	var b strings.Builder
	for i := 0; i < fileCount; i++ {
		path := fmt.Sprintf("generated/file-%03d.txt", i)
		lineCount := 6
		if (i+1)%10 == 0 {
			lineCount = autoLoadFileDiffLines + 50
		}
		b.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1,1 +1,%d @@\n-old\n", path, path, path, path, lineCount))
		for j := 0; j < lineCount; j++ {
			b.WriteString(fmt.Sprintf("+generated line %03d for file %03d\n", j, i))
		}
	}
	return b.String()
}

func TestDiffViewer_BlockedPlaceholderSupportsCollapse(t *testing.T) {
	diff := "diff --git a/huge.txt b/huge.txt\n--- a/huge.txt\n+++ b/huge.txt\n@@ -0,0 +1,25000 @@\n" + strings.Repeat("+line\n", maxLoadableFileDiffLines+5)

	var buf bytes.Buffer
	if err := DiffViewer(diff).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	if !strings.Contains(body, "Diff not available") || !strings.Contains(body, "single-file limit") {
		t.Error("expected blocked placeholder reason for oversized single file")
	}
	if !strings.Contains(body, `onclick="toggleDiffFile(0)"`) {
		t.Error("expected blocked placeholder header to toggle collapse")
	}
	if !strings.Contains(body, `data-diff-toggle="0"`) {
		t.Error("expected blocked placeholder header to participate in persisted collapse state")
	}
	if !strings.Contains(body, `id="diff-chevron-0"`) || !strings.Contains(body, `id="diff-chevron-split-0"`) {
		t.Error("expected blocked placeholder cards to render inline and split chevrons")
	}
	if !strings.Contains(body, `id="diff-body-0"`) || !strings.Contains(body, `id="diff-body-split-0"`) {
		t.Error("expected blocked placeholders to expose collapsible body targets")
	}
	if strings.Contains(body, `class="diff-file-body overflow-hidden transition-all duration-300 ease-in-out p-6`) {
		t.Error("blocked placeholder body should not keep padding on the collapsed element")
	}
	if strings.Contains(body, "Load diff") {
		t.Error("blocked placeholder should not render load button")
	}
	if strings.Contains(body, `<table class="diff-table`) {
		t.Error("blocked placeholder should not mount heavy diff table DOM")
	}
}

func TestLoadDiffFileCard_BlockedFileShowsReason(t *testing.T) {
	diff := "diff --git a/huge.txt b/huge.txt\n--- a/huge.txt\n+++ b/huge.txt\n@@ -0,0 +1,25000 @@\n" + strings.Repeat("+line\n", maxLoadableFileDiffLines+5)
	var buf bytes.Buffer
	if err := LoadDiffFileCard(diff, 0, "inline", "task123", nil, true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()
	if !strings.Contains(body, "single-file limit") {
		t.Error("expected blocked load reason for oversized single file")
	}
}

func TestDiffViewerChangedFilesBadgesStayContained(t *testing.T) {
	diff := `diff --git a/src/short.go b/src/short.go
--- a/src/short.go
+++ b/src/short.go
@@ -1 +1,2 @@
 package src
+var Short = true
diff --git a/web/templates/components/really-long-directory-name/another-long-directory-name/this-is-an-extremely-long-file-name-that-must-not-overflow-the-task-changes-changed-files-pill-container.go b/web/templates/components/really-long-directory-name/another-long-directory-name/this-is-an-extremely-long-file-name-that-must-not-overflow-the-task-changes-changed-files-pill-container.go
--- a/web/templates/components/really-long-directory-name/another-long-directory-name/this-is-an-extremely-long-file-name-that-must-not-overflow-the-task-changes-changed-files-pill-container.go
+++ b/web/templates/components/really-long-directory-name/another-long-directory-name/this-is-an-extremely-long-file-name-that-must-not-overflow-the-task-changes-changed-files-pill-container.go
@@ -1 +1,2 @@
 package components
+var Long = true
`

	for name, render := range map[string]func() (string, error){
		"DiffViewer": func() (string, error) {
			var buf bytes.Buffer
			err := DiffViewer(diff).Render(context.Background(), &buf)
			return buf.String(), err
		},
		"DiffViewerWithReview": func() (string, error) {
			var buf bytes.Buffer
			err := DiffViewerWithReview(diff, "task123", nil).Render(context.Background(), &buf)
			return buf.String(), err
		},
	} {
		t.Run(name, func(t *testing.T) {
			body, err := render()
			if err != nil {
				t.Fatalf("render failed: %v", err)
			}
			if !strings.Contains(body, `<div class="mb-4 p-3 bg-base-200 rounded-lg min-w-0 max-w-full overflow-hidden">`) {
				t.Fatal("changed files card must constrain horizontal overflow")
			}
			if !strings.Contains(body, `<div class="flex flex-wrap gap-1 min-w-0 max-w-full overflow-hidden">`) {
				t.Fatal("changed files badge group must wrap within its container")
			}
			if !strings.Contains(body, `class="badge badge-sm badge-outline cursor-pointer hover:badge-primary max-w-full min-w-0 overflow-hidden"`) {
				t.Fatal("changed file badge must be constrained to the available width")
			}
			if !strings.Contains(body, `<span class="block truncate min-w-0">.../another-long-directory-name/this-is-an-extremely-long-file-name-that-must-not-overflow-the-task-changes-changed-files-pill-container.go</span>`) {
				t.Fatal("changed file badge label must truncate inside the pill")
			}
		})
	}
}

// TestDiffViewerToolbar_ResponsiveNonOverlappingLayout verifies the toolbar
// layout classes that caused controls to visually overlap.
//
// Root-cause: the outer wrapper used gap-y-2 (vertical gap only — zero column
// gap), the left group used flex-1 min-w-0 (could shrink to 0 px), and the
// right group used flex-shrink-0 ml-auto. On narrow viewports the left group
// shrank to ~0 px while the right group stayed at its natural width starting
// at x≈0, placing "files changed", "Submit Review", and "Inline/Split" all at
// the same horizontal position — a visible overlap.

func TestDiffViewerToolbar_ResponsiveNonOverlappingLayout(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
 func main() {
 }
`
	t.Run("DiffViewerWithReview toolbar uses gap-2 not gap-y-2", func(t *testing.T) {
		var buf bytes.Buffer
		if err := DiffViewerWithReview(diff, "task123", nil).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render failed: %v", err)
		}
		body := buf.String()
		// gap-y-2 adds only a row gap (no column gap between left and right groups
		// on the same line). gap-2 adds both row and column gaps so controls are
		// separated even when sharing a row.
		if strings.Contains(body, `class="flex flex-wrap items-center gap-y-2 mb-4"`) {
			t.Error("toolbar outer container must not use gap-y-2 (vertical-only gap); use gap-2 so left and right control groups have a horizontal separator")
		}
		if !strings.Contains(body, `class="flex flex-wrap items-center gap-2 mb-4"`) {
			t.Error("toolbar outer container must use gap-2 to add horizontal gap between control groups")
		}
	})

	t.Run("DiffViewerWithReview left group does not use min-w-0", func(t *testing.T) {
		var buf bytes.Buffer
		if err := DiffViewerWithReview(diff, "task123", nil).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render failed: %v", err)
		}
		body := buf.String()
		// min-w-0 overrides the group's natural minimum content width to 0, which
		// allows the browser to shrink the "files changed" group to 0 px. That in
		// turn lets the right group start at x=0, visually overlapping.
		if strings.Contains(body, `flex-1 min-w-0`) {
			t.Error("toolbar left group must not use min-w-0: it allows the group to shrink to 0 px, causing all controls to stack at the left edge on narrow viewports")
		}
	})

	t.Run("DiffViewerWithReview right group does not use flex-shrink-0 ml-auto", func(t *testing.T) {
		var buf bytes.Buffer
		if err := DiffViewerWithReview(diff, "task123", nil).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render failed: %v", err)
		}
		body := buf.String()
		// flex-shrink-0 prevents the right group from shrinking. Combined with
		// min-w-0 on the left, the right group overflows at x=0 when the container
		// is narrower than the right group's natural width, causing all controls to
		// appear at the same horizontal position (overlap).
		if strings.Contains(body, `flex-shrink-0 ml-auto`) {
			t.Error("toolbar right group must not use flex-shrink-0 ml-auto: causes overflow-at-x=0 overlap with the left group on narrow viewports")
		}
	})

	t.Run("DiffViewer (no review) toolbar uses gap-2 not gap-y-2", func(t *testing.T) {
		var buf bytes.Buffer
		if err := DiffViewer(diff).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render failed: %v", err)
		}
		body := buf.String()
		if strings.Contains(body, `class="flex flex-wrap items-center gap-y-2 mb-4"`) {
			t.Error("toolbar outer container must not use gap-y-2; use gap-2 for horizontal and vertical gap")
		}
		if !strings.Contains(body, `class="flex flex-wrap items-center gap-2 mb-4"`) {
			t.Error("toolbar outer container must use gap-2")
		}
	})

	t.Run("DiffViewer (no review) left group does not use min-w-0", func(t *testing.T) {
		var buf bytes.Buffer
		if err := DiffViewer(diff).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render failed: %v", err)
		}
		body := buf.String()
		if strings.Contains(body, `flex-1 min-w-0`) {
			t.Error("toolbar left group must not use min-w-0: allows the group to shrink to 0 px causing overlap")
		}
	})

	t.Run("DiffViewer (no review) join div does not use flex-shrink-0 ml-auto", func(t *testing.T) {
		var buf bytes.Buffer
		if err := DiffViewer(diff).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render failed: %v", err)
		}
		body := buf.String()
		if strings.Contains(body, `flex-shrink-0 ml-auto`) {
			t.Error("toolbar join div must not use flex-shrink-0 ml-auto: causes overlap on narrow viewports")
		}
	})
}
