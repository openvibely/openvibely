package skilldeps

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Archives are expanded into an unpublished staging directory. Links are created
// last, so an archive entry can never write through an earlier symlink.
func prepare(ctx context.Context, p Package, archive, root string) error {
	if p.Format == "deb-bundle" {
		return prepareDEBs(ctx, p, archive, root)
	}
	if p.Format == "msi" {
		return prepareMSI(ctx, p, archive, root)
	}
	if p.Format == "dmg" {
		return prepareDMG(ctx, archive, root, p.Executable)
	}
	if p.Format == "raw" {
		dst := filepath.Join(root, filepath.FromSlash(p.Executable))
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			return err
		}
		return os.Rename(archive, dst)
	}
	x := extractor{ctx: ctx, root: root, links: map[string]string{}}
	switch p.Format {
	case "zip":
		reader, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer reader.Close()
		for _, file := range reader.File {
			if file.UncompressedSize64 > 8<<30 {
				return errors.New("archive entry exceeds limit")
			}
			stream, err := file.Open()
			if err != nil {
				return err
			}
			err = x.entry(file.Name, file.Mode(), int64(file.UncompressedSize64), stream, "")
			stream.Close()
			if err != nil {
				return err
			}
		}
	case "tar.gz":
		file, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer file.Close()
		gz, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer gz.Close()
		reader := tar.NewReader(gz)
		for {
			h, err := reader.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeSymlink {
				return fmt.Errorf("unsupported archive entry type %d", h.Typeflag)
			}
			if err := x.entry(h.Name, h.FileInfo().Mode(), h.Size, reader, h.Linkname); err != nil {
				return err
			}
		}
	default:
		return errors.New("unsupported archive format")
	}
	return x.finishLinks()
}

func (x *extractor) finishLinks() error {
	root, err := filepath.EvalSymlinks(x.root)
	if err != nil {
		return err
	}
	for name, target := range x.links {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			return err
		}
		if err := os.Symlink(filepath.FromSlash(target), filepath.Join(root, name)); err != nil {
			return err
		}
	}
	// Verify chained symlinks too, rejecting cycles, dangling links and escapes.
	for name := range x.links {
		resolved, err := filepath.EvalSymlinks(filepath.Join(root, name))
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("archive symlink escapes package")
		}
	}
	return nil
}

type extractor struct {
	ctx   context.Context
	root  string
	count int
	bytes int64
	links map[string]string
}

func (x *extractor) entry(name string, mode os.FileMode, size int64, reader io.Reader, target string) error {
	if err := x.ctx.Err(); err != nil {
		return err
	}
	name = strings.TrimSuffix(strings.TrimPrefix(name, "./"), "/")
	if name == "" || name == "." {
		return nil
	}
	if !safeRelative(name) {
		return fmt.Errorf("unsafe archive path %q", name)
	}
	x.count++
	x.bytes += size
	if x.count > 200000 || size < 0 || x.bytes > 8<<30 {
		return errors.New("archive expansion limit exceeded")
	}
	dst := filepath.Join(x.root, filepath.FromSlash(name))
	if mode.IsDir() {
		return os.MkdirAll(dst, 0755)
	}
	if mode&os.ModeSymlink != 0 {
		if target == "" {
			b, err := io.ReadAll(io.LimitReader(reader, 4097))
			if err != nil {
				return err
			}
			if len(b) > 4096 {
				return errors.New("symlink target too long")
			}
			target = string(b)
		}
		if target == "" || strings.ContainsAny(target, "\\:\x00") || strings.HasPrefix(target, "/") {
			return errors.New("unsafe archive symlink")
		}
		resolved := path.Clean(path.Join(path.Dir(name), target))
		if !safeRelative(resolved) {
			return errors.New("archive symlink escapes package")
		}
		if _, exists := x.links[name]; exists {
			return errors.New("duplicate archive symlink")
		}
		x.links[name] = target
		return nil
	}
	if !mode.IsRegular() {
		return errors.New("unsupported archive file type")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(file, io.LimitReader(reader, size+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size {
		return errors.New("archive entry size mismatch")
	}
	return os.Chmod(dst, mode.Perm()&0755|0400)
}

func prepareDMG(ctx context.Context, archive, root, executable string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("DMG preparation requires macOS")
	}
	app := strings.Split(executable, "/")[0]
	if !strings.HasSuffix(app, ".app") {
		return errors.New("DMG executable must be within an app bundle")
	}
	mount, err := os.MkdirTemp(filepath.Dir(root), ".mount-")
	if err != nil {
		return err
	}
	defer os.Remove(mount)
	cmd := exec.CommandContext(ctx, "/usr/bin/hdiutil", "attach", "-readonly", "-nobrowse", "-quiet", "-mountpoint", mount, archive)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mount dependency DMG: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "/usr/bin/hdiutil", "detach", "-quiet", mount).Run()
	}()
	if err := exec.CommandContext(ctx, "/usr/bin/ditto", filepath.Join(mount, app), filepath.Join(root, app)).Run(); err != nil {
		return fmt.Errorf("copy app bundle: %w", err)
	}
	return nil
}
