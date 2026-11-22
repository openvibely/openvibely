package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStagedBundleIntegrity(t *testing.T) {
	for _, damage := range []string{"content", "mode", "symlink", "missing", "extra"} {
		t.Run(damage, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "binary")
			link := filepath.Join(root, "link")
			if err := os.WriteFile(binary, []byte("original"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("binary", link); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			digest, err := stagedContentDigest(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			stage := LocalStagedUpdate{ArtifactPath: root, Version: "0.7.0", ContentDigest: digest}
			release := VerifiedRelease{Metadata: ReleaseMetadata{Version: "0.7.0"}}
			if !stagedUpdateReusable(ctx, stage, release) {
				t.Fatal("unchanged bundle rejected")
			}
			switch damage {
			case "content":
				err = os.WriteFile(binary, []byte("modified"), 0755)
			case "mode":
				// Windows represents the writable/read-only bit, not Unix execute bits.
				t.Cleanup(func() { _ = os.Chmod(binary, 0755) })
				err = os.Chmod(binary, 0444)
			case "symlink":
				if err = os.Remove(link); err == nil {
					err = os.Symlink("elsewhere", link)
				}
			case "missing":
				err = os.Remove(binary)
			case "extra":
				err = os.WriteFile(filepath.Join(root, "extra"), []byte("extra"), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if stagedUpdateReusable(ctx, stage, release) {
				t.Fatal("changed bundle reused")
			}
		})
	}
}
