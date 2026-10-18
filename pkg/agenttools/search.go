package agenttools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// SearchFiles searches without following directory symlinks. Every discovered file
// passes both the caller's scope policy and the current managed-file authorization
// before its contents are read.
func SearchFiles(ctx context.Context, root, pattern string, opts SearchOptions) ([]string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}
	var matches []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
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
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		display := path
		if opts.DisplayPath != nil {
			display = opts.DisplayPath(path)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if err := ctx.Err(); err != nil {
				return err
			}
			if re.MatchString(line) {
				if opts.MaxLineLength > 0 {
					line = truncateLine(line, opts.MaxLineLength)
				}
				matches = append(matches, fmt.Sprintf("%s:%d: %s", display, i+1, line))
				if opts.MaxResults > 0 && len(matches) >= opts.MaxResults {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return matches, err
}
