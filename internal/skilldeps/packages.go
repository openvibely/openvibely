package skilldeps

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Extract payloads only: no dpkg installation, maintainer scripts, or host writes.
func prepareDEBs(ctx context.Context, p Package, archive, root string) error {
	if runtime.GOOS != "linux" {
		return errors.New("DEB preparation requires Linux")
	}
	decoder, err := exec.LookPath("dpkg-deb")
	if err != nil {
		return errors.New("DEB cache extraction requires dpkg-deb")
	}
	packages, err := os.MkdirTemp(filepath.Dir(root), ".debs-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(packages)
	if err := prepare(ctx, Package{Format: "tar.gz"}, archive, packages); err != nil {
		return err
	}
	x := extractor{ctx: ctx, root: root, links: map[string]string{}}
	count := 0
	err = filepath.WalkDir(packages, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".deb") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("DEB payload must be a regular file")
		}
		count++
		cmd := exec.CommandContext(ctx, decoder, "--fsys-tarfile", path)
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			return err
		}
		if err = cmd.Start(); err != nil {
			return err
		}
		extractErr := extractDEBPayload(&x, pipe)
		if extractErr != nil {
			_ = cmd.Process.Kill()
		}
		_ = pipe.Close()
		waitErr := cmd.Wait()
		if extractErr != nil {
			return extractErr
		}
		if waitErr != nil {
			return fmt.Errorf("decode DEB: %w", waitErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if count == 0 || x.count == 0 {
		return errors.New("archive contains no matching DEB payload")
	}
	return x.finishLinks()
}

func extractDEBPayload(x *extractor, source io.Reader) error {
	reader := tar.NewReader(source)
	for {
		h, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := x.ctx.Err(); err != nil {
			return err
		}
		name := strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/")
		if name == "" || name == "." {
			continue
		}
		if !safeRelative(name) {
			return fmt.Errorf("unsafe DEB payload path %q", name)
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeSymlink {
			return fmt.Errorf("unsupported DEB payload type %d", h.Typeflag)
		}
		target := h.Linkname
		// DEB absolute links refer to the package filesystem root. Rebase them
		// inside the cache rather than linking to the host filesystem.
		if h.Typeflag == tar.TypeSymlink && strings.HasPrefix(target, "/") {
			targetPath := strings.TrimPrefix(target, "/")
			if !safeRelative(targetPath) {
				return fmt.Errorf("unsafe DEB link target %q", target)
			}
			target, err = filepath.Rel(filepath.Dir(filepath.FromSlash(name)), filepath.FromSlash(targetPath))
			if err != nil {
				return err
			}
			target = filepath.ToSlash(target)
		}
		if err := x.entry(name, h.FileInfo().Mode(), h.Size, reader, target); err != nil {
			return err
		}
	}
}

// MSI administrative extraction expands files into a private cache directory;
// it does not register the package as an installed product or request a reboot.
func prepareMSI(ctx context.Context, p Package, archive, root string) error {
	if runtime.GOOS != "windows" {
		return errors.New("MSI preparation requires Windows")
	}
	staging, err := os.MkdirTemp(filepath.Dir(root), ".msi-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	msi := archive + ".msi"
	if err := os.Rename(archive, msi); err != nil {
		return err
	}
	defer os.Remove(msi)
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" || !filepath.IsAbs(systemRoot) {
		return errors.New("Windows SystemRoot is unavailable")
	}
	cmd := exec.CommandContext(ctx, filepath.Join(systemRoot, "System32", "msiexec.exe"), "/a", msi, "/qn", "/norestart", "TARGETDIR="+staging)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("extract MSI: %w", err)
	}
	return movePackageTree(staging, root, p)
}

// Root discovery is opt-in and matches the declared relative executable path.
func movePackageTree(staging, root string, p Package) error {
	var found []string
	err := filepath.WalkDir(staging, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("unexpected link in MSI administrative image")
		}
		if !entry.IsDir() && p.FindRoot {
			rel, err := filepath.Rel(staging, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if strings.EqualFold(rel, p.Executable) || strings.HasSuffix(strings.ToLower(rel), "/"+strings.ToLower(p.Executable)) {
				candidate := path
				for range strings.Split(p.Executable, "/") {
					candidate = filepath.Dir(candidate)
				}
				found = append(found, candidate)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !p.FindRoot {
		found = []string{staging}
	}
	if len(found) != 1 {
		return fmt.Errorf("expected one package root containing the declared executable, found %d", len(found))
	}
	entries, err := os.ReadDir(found[0])
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.Rename(filepath.Join(found[0], entry.Name()), filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
