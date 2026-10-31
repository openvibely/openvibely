package components

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

const diffChunkRows = 200

// DiffCodeRevision identifies only code; review mutations cannot invalidate chunk URLs.
func DiffCodeRevision(file DiffFile) string {
	return DiffFileRevision(file, nil)
}

// File revisions include review annotations so unchanged cards can survive refreshes.
func DiffFileRevision(file DiffFile, comments []models.ReviewComment) string {
	relevant := make([]models.ReviewComment, 0)
	for _, comment := range comments {
		if comment.FilePath == file.Path {
			relevant = append(relevant, comment)
		}
	}
	data, _ := json.Marshal(struct {
		File     DiffFile
		Comments []models.ReviewComment
	}{file, relevant})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func progressiveDiffMetas(files []DiffFile, comments []models.ReviewComment) []DiffFileRenderMeta {
	result := make([]DiffFileRenderMeta, 0, len(files))
	for index, file := range files {
		result = append(result, DiffFileRenderMeta{Index: index, File: file, Revision: DiffFileRevision(file, comments), CodeRevision: DiffCodeRevision(file)})
	}
	return result
}

// DiffFileChunks keeps split pairs together, even when one replacement block
// spans several pages. Original line numbers remain the review identities.
func DiffFileChunks(file DiffFile, view string) []DiffFile {
	chunks := make([]DiffFile, 0)
	current := file
	current.Hunks = nil
	rows, bytes := 0, 0
	flush := func() {
		if len(current.Hunks) > 0 {
			chunks = append(chunks, current)
			current.Hunks = nil
			rows, bytes = 0, 0
		}
	}
	for _, hunk := range file.Hunks {
		var units [][]DiffLine
		var pairs []SplitPair
		if view == "split" {
			pairs = buildSplitLines(hunk.Lines)
			for _, pair := range pairs {
				var lines []DiffLine
				if pair.Left.Type == "ctx" {
					lines = append(lines, pair.Left)
				} else {
					if pair.Left.Type != "empty" {
						lines = append(lines, pair.Left)
					}
					if pair.Right.Type != "empty" {
						lines = append(lines, pair.Right)
					}
				}
				units = append(units, lines)
			}
		} else {
			for _, line := range hunk.Lines {
				units = append(units, []DiffLine{line})
			}
		}
		part := DiffHunk{Header: hunk.Header}
		appendPart := func() {
			if len(part.Lines) > 0 {
				current.Hunks = append(current.Hunks, part)
				part = DiffHunk{Header: hunk.Header}
			}
		}
		for index, unit := range units {
			size := 0
			for _, line := range unit {
				size += len(line.Content)
			}
			if rows >= diffChunkRows || (bytes+size > 64*1024 && rows > 0) {
				appendPart()
				flush()
			}
			part.Lines = append(part.Lines, unit...)
			if view == "split" {
				part.SplitPairs = append(part.SplitPairs, pairs[index])
			}
			rows++
			bytes += size
		}
		appendPart()
	}
	flush()
	if len(chunks) == 0 {
		chunks = append(chunks, file)
	}
	return chunks
}

// Preserve exact alignment when a chunk cuts through unequal replacement runs.
func splitHunkLines(hunk DiffHunk) []SplitPair {
	if hunk.SplitPairs != nil {
		return hunk.SplitPairs
	}
	return buildSplitLines(hunk.Lines)
}

// DiffChunkContainingLine resolves a reading anchor in the destination layout.
// Prefer the new side, matching the browser's row restoration, then the old side
// for deleted lines or anchors whose new side is absent.
func DiffChunkContainingLine(chunks []DiffFile, oldLine, newLine int) (int, bool) {
	for _, side := range []struct {
		line int
		new  bool
	}{{newLine, true}, {oldLine, false}} {
		if side.line <= 0 {
			continue
		}
		for index, chunk := range chunks {
			for _, hunk := range chunk.Hunks {
				for _, line := range hunk.Lines {
					if (side.new && line.NewNum == side.line) || (!side.new && line.OldNum == side.line) {
						return index, true
					}
				}
			}
		}
	}
	return 0, false
}
