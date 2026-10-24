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
func prepareOfficeDEBs(ctx context.Context, archive, root string) error {
	if runtime.GOOS != "linux" {
		return errors.New("LibreOffice DEB preparation requires Linux")
	}
	decoder, err := exec.LookPath("dpkg-deb")
	if err != nil {
		return errors.New("LibreOffice cache extraction requires dpkg-deb; install it or set OPENVIBELY_TOOL_LIBREOFFICE to an installed executable")
	}
	packages, err := os.MkdirTemp(filepath.Dir(root), ".office-debs-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(packages)
	if err := prepare(ctx, Package{Format: "tar.gz"}, archive, packages); err != nil {
		return err
	}
	x := extractor{ctx: ctx, root: root, links: map[string]string{}}
	count := 0
	prefix := ""
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
		extractErr := extractOfficePayload(&x, pipe, &prefix)
		if extractErr != nil {
			_ = cmd.Process.Kill()
		}
		_ = pipe.Close()
		waitErr := cmd.Wait()
		if extractErr != nil {
			return extractErr
		}
		if waitErr != nil {
			return fmt.Errorf("decode LibreOffice DEB: %w", waitErr)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if count == 0 || prefix == "" {
		return errors.New("archive contains no LibreOffice DEB payload")
	}
	return x.finishLinks()
}

func extractOfficePayload(x *extractor, source io.Reader, prefix *string) error {
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
		parts := strings.SplitN(name, "/", 3)
		// Desktop registration and package-manager integration stay outside the cache.
		if len(parts) < 2 || parts[0] != "opt" || !strings.HasPrefix(parts[1], "libreoffice") {
			continue
		}
		if !safeRelative(name) {
			return fmt.Errorf("unsafe LibreOffice payload path %q", name)
		}
		if *prefix == "" {
			*prefix = parts[1]
		} else if *prefix != parts[1] {
			return errors.New("multiple LibreOffice installation roots")
		}
		if len(parts) < 3 {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeSymlink {
			return fmt.Errorf("unsupported LibreOffice payload type %d", h.Typeflag)
		}
		if err := x.entry(parts[2], h.FileInfo().Mode(), h.Size, reader, h.Linkname); err != nil {
			return err
		}
	}
}

// MSI administrative extraction expands files into a private cache directory;
// it does not register LibreOffice as an installed product or request a reboot.
func prepareOfficeMSI(ctx context.Context, archive, root string) error {
	if runtime.GOOS != "windows" {
		return errors.New("LibreOffice MSI preparation requires Windows")
	}
	staging, err := os.MkdirTemp(filepath.Dir(root), ".office-msi-")
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
		return fmt.Errorf("extract LibreOffice MSI: %w", err)
	}
	return moveOfficeTree(staging, root)
}

// MSI versions may prepend a product directory. Locate the actual program tree
// instead of baking Windows Installer's administrative-image layout into YAML.
func moveOfficeTree(staging, root string) error {
	var found []string
	err := filepath.WalkDir(staging, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("unexpected link in MSI administrative image")
		}
		if !entry.IsDir() && strings.EqualFold(entry.Name(), "soffice.exe") && strings.EqualFold(filepath.Base(filepath.Dir(path)), "program") {
			found = append(found, filepath.Dir(filepath.Dir(path)))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(found) != 1 {
		return fmt.Errorf("expected one LibreOffice program tree, found %d", len(found))
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
