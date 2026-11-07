package pageplugins

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	MaxPackageFiles = 2000
	MaxPackageBytes = 64 << 20
)

// SafeJoin resolves rel inside root and refuses symlink escapes.
func SafeJoin(root, rel string) (string, error) {
	clean, err := CleanRelPath(rel)
	if err != nil {
		return "", err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	full := filepath.Join(realRoot, filepath.FromSlash(clean))
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	if resolved != realRoot && !strings.HasPrefix(resolved, realRoot+string(filepath.Separator)) {
		return "", errors.New("path escapes package root")
	}
	return resolved, nil
}

// HashPrefix is versioned so hashes from older formats never match current content.
const HashPrefix = "sha256-v2:"

// HashTree hashes every regular file as length-prefixed path + per-file digest. Any
// symlink or special file is rejected so the hash covers what will be served/executed.
func HashTree(fsys fs.FS) (string, error) {
	var files []string
	var total int64
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed in plugin packages: %s", p)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unsupported file type: %s", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		files = append(files, p)
		if len(files) > MaxPackageFiles || total > MaxPackageBytes {
			return errors.New("plugin package is too large")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	h.Write([]byte("openvibely-page-plugin-tree-v2\x00"))
	var n [8]byte
	for _, p := range files {
		f, err := fsys.Open(p)
		if err != nil {
			return "", err
		}
		fh := sha256.New()
		_, err = io.Copy(fh, f)
		f.Close()
		if err != nil {
			return "", err
		}
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write([]byte(p))
		h.Write(fh.Sum(nil))
	}
	return HashPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// copyTree copies a validated package into dst, preserving the executable bit only.
func copyTree(fsys fs.FS, dst string) error {
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unsupported file type: %s", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		src, err := fsys.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, src); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

func readManifestFS(fsys fs.FS) (*Manifest, error) {
	f, err := fsys.Open(ManifestFile)
	if err != nil {
		return nil, fmt.Errorf("plugin.json not found: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, MaxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	return ParseManifest(raw)
}

// validatePackageFS checks the manifest and that every referenced file exists inside the package.
func validatePackageFS(fsys fs.FS) (*Manifest, string, error) {
	m, err := readManifestFS(fsys)
	if err != nil {
		return nil, "", err
	}
	hash, err := HashTree(fsys)
	if err != nil {
		return nil, "", err
	}
	for _, p := range m.Pages {
		clean, _ := CleanRelPath(p.Entry)
		if st, err := fs.Stat(fsys, clean); err != nil || !st.Mode().IsRegular() {
			return nil, "", fmt.Errorf("page %q entry %q not found in package", p.ID, p.Entry)
		}
	}
	if m.Backend != nil {
		for platform, exe := range m.Backend.Executables {
			clean, _ := CleanRelPath(exe)
			if st, err := fs.Stat(fsys, clean); err != nil || !st.Mode().IsRegular() {
				return nil, "", fmt.Errorf("backend executable for %s (%q) not found in package", platform, exe)
			}
		}
	}
	return m, hash, nil
}
