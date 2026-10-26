package agenttools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// SearchOptions preserves each caller's directory policy and output limits.
// AllowFile is an additional restriction; context authorization always applies.
type SearchOptions struct {
	Include          string
	MaxResults       int
	MaxLineLength    int
	SkipDirectory    func(string) bool
	AllowFile        func(string) bool
	DisplayPath      func(string) string
	IgnoreWalkErrors bool
}

var searchBinaryExtensions = map[string]bool{
	".exe": true, ".bin": true, ".so": true, ".dylib": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".zip": true, ".tar": true, ".gz": true, ".pdf": true,
	".wasm": true, ".o": true, ".a": true,
}

const (
	binaryCheckSize     = 8 * 1024
	binaryTailCheckSize = 32 * 1024
)

// SearchFiles searches without following directory symlinks. Every discovered file
// passes both the caller's scope policy and the current managed-file authorization
// before its contents are read. Files are streamed line by line, and files
// containing NUL bytes are skipped as binary.
func SearchFiles(ctx context.Context, root, pattern string, opts SearchOptions) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	s := fileSearcher{
		ctx:          ctx,
		re:           re,
		opts:         opts,
		reader:       bufio.NewReaderSize(nil, 32*1024),
		lineBuffer:   make([]byte, 0, 128),
		binarySample: make([]byte, binaryTailCheckSize),
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == root {
				return fmt.Errorf("search path %q: %w", root, walkErr)
			}
			if opts.IgnoreWalkErrors {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			if opts.SkipDirectory != nil && opts.SkipDirectory(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if s.full() {
			return filepath.SkipAll
		}
		if opts.Include != "" {
			matched, _ := filepath.Match(opts.Include, entry.Name())
			if !matched {
				return nil
			}
		}
		if opts.AllowFile != nil && !opts.AllowFile(path) {
			return nil
		}
		if allow, ok := ctx.Value(fileReadAuthorizationKey{}).(func(context.Context, string) bool); ok && !allow(ctx, path) {
			return nil
		}
		if err := s.searchFile(path); err != nil {
			return err
		}
		if s.full() {
			return filepath.SkipAll
		}
		return nil
	})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return s.matches, err
}

type fileSearcher struct {
	ctx          context.Context
	re           *regexp.Regexp
	opts         SearchOptions
	reader       *bufio.Reader
	lineBuffer   []byte
	binarySample []byte
	matches      []string
	fileMatches  []string
}

func (s *fileSearcher) full() bool {
	return s.opts.MaxResults > 0 && len(s.matches) >= s.opts.MaxResults
}

// searchFile stages matches until EOF so a NUL byte found mid-stream discards
// the whole file. Only context errors are returned; unreadable files are skipped.
func (s *fileSearcher) searchFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	containsNUL, err := containsNULInFileSamples(file, info.Size(), s.binarySample)
	if err != nil || containsNUL {
		return nil
	}
	s.reader.Reset(file)

	display := path
	if s.opts.DisplayPath != nil {
		display = s.opts.DisplayPath(path)
	}
	remaining := -1
	if s.opts.MaxResults > 0 {
		remaining = s.opts.MaxResults - len(s.matches)
	}
	s.lineBuffer = s.lineBuffer[:0]
	s.fileMatches = s.fileMatches[:0]
	lineNumber := 0
	lastWasNewline := true // strings.Split treats an empty file as one empty line.
	matchLine := func(line []byte) {
		lineNumber++
		if (remaining < 0 || len(s.fileMatches) < remaining) && s.re.Match(line) {
			text := string(line)
			if s.opts.MaxLineLength > 0 {
				text = truncateLine(text, s.opts.MaxLineLength)
			}
			s.fileMatches = append(s.fileMatches, fmt.Sprintf("%s:%d: %s", display, lineNumber, text))
		}
	}
	for {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		fragment, readErr := s.reader.ReadSlice('\n')
		if bytes.IndexByte(fragment, 0) >= 0 {
			return nil
		}
		switch readErr {
		case nil:
			line := fragment[:len(fragment)-1]
			if len(s.lineBuffer) > 0 {
				s.lineBuffer = append(s.lineBuffer, line...)
				line = s.lineBuffer
			}
			matchLine(line)
			s.lineBuffer = s.lineBuffer[:0]
			lastWasNewline = true
		case bufio.ErrBufferFull:
			s.lineBuffer = append(s.lineBuffer, fragment...)
			lastWasNewline = false
		case io.EOF:
			if len(s.lineBuffer) > 0 {
				s.lineBuffer = append(s.lineBuffer, fragment...)
				fragment = s.lineBuffer
			}
			if len(fragment) > 0 || lastWasNewline {
				matchLine(fragment)
			}
			s.matches = append(s.matches, s.fileMatches...)
			return nil
		default:
			return nil
		}
	}
}

func containsNULInFileSamples(file io.ReaderAt, size int64, buffer []byte) (bool, error) {
	if size <= 0 || len(buffer) == 0 {
		return false, nil
	}

	prefixSize := int64(binaryCheckSize)
	if prefixSize > size {
		prefixSize = size
	}
	if prefixSize > int64(len(buffer)) {
		prefixSize = int64(len(buffer))
	}
	read, err := file.ReadAt(buffer[:int(prefixSize)], 0)
	if bytes.IndexByte(buffer[:read], 0) >= 0 {
		return true, nil
	}
	if err != nil && err != io.EOF {
		return false, err
	}

	tailStart := size - int64(len(buffer))
	if tailStart < prefixSize {
		tailStart = prefixSize
	}
	if tailStart >= size {
		return false, nil
	}
	tailSize := size - tailStart
	read, err = file.ReadAt(buffer[:int(tailSize)], tailStart)
	if bytes.IndexByte(buffer[:read], 0) >= 0 {
		return true, nil
	}
	if err != nil && err != io.EOF {
		return false, err
	}
	return false, nil
}

func truncateLine(line string, maxLen int) string {
	if len(line) <= maxLen {
		return line
	}
	return line[:maxLen] + "..."
}
