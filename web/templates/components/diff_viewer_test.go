package components

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
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
	var buf bytes.Buffer
	err := DiffViewer(diff).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	body := buf.String()

	// File name visible
	if !strings.Contains(body, "main.go") {
		t.Error("expected file name 'main.go' to be visible")
	}
	// Stats visible (+1 -0)
	if !strings.Contains(body, "+1 -0") {
		t.Error("expected diff stats '+1 -0' to be visible")
	}
	// Chevron SVG present (right-pointing arrow path)
	if !strings.Contains(body, "M9 5l7 7-7 7") {
		t.Error("expected chevron SVG path")
	}
	// Click handler present
	if !strings.Contains(body, "data-diff-toggle") {
		t.Error("expected data-diff-toggle attribute on header")
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
