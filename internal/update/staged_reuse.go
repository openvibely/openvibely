package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Metadata can be renewed without changing the artifact. Never reuse a staged
// artifact across versions, channels, or changes to its signed target.
func sameStagedRelease(a, b *VerifiedRelease) bool {
	return a != nil && b != nil && a.Metadata.Version == b.Metadata.Version &&
		a.Metadata.Channel == b.Metadata.Channel && a.Metadata.Commit == b.Metadata.Commit &&
		a.Target == b.Target && a.Action == b.Action && a.ApplySupported == b.ApplySupported
}

func stagedUpdateReusable(ctx context.Context, staged any, release VerifiedRelease) bool {
	switch value := staged.(type) {
	case LocalStagedUpdate:
		if value.Version != release.Metadata.Version || value.ContentDigest == "" {
			return false
		}
		digest, err := stagedContentDigest(ctx, value.ArtifactPath)
		return err == nil && digest == value.ContentDigest
	case VerifiedRelease:
		return sameStagedRelease(&value, &release)
	default:
		return false
	}
}

// The signed archive digest verifies the download; this separate digest checks
// that the extracted file/bundle has not changed since staging. It survives
// restarts with LocalStagedUpdate. Older staged records without it are restaged.
// Include paths, modes, file bytes and symlink destinations without following
// symlinks, so complete macOS app bundles can be checked as well as binaries.
func stagedContentDigest(ctx context.Context, root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", fmt.Errorf("invalid staged artifact type: %s", root)
	}
	hash := sha256.New()
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%q %d\n", filepath.ToSlash(relative), info.Mode())
		switch {
		case info.Mode().IsRegular():
			fmt.Fprintf(hash, "%d\n", info.Size())
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(hash, "%q\n", target)
		case info.IsDir():
		default:
			return fmt.Errorf("unsupported staged file type: %s", path)
		}
		return ctx.Err()
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
