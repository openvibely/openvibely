package filecontent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// NativeImagePreview converts HEIC/HEIF into a browser-readable thumbnail.
// Originals remain unchanged for downloads. Like the reference desktop path,
// this uses the host image decoder rather than pretending Chromium decodes HEIC.
func NativeImagePreview(ctx context.Context, data []byte) ([]byte, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return nil, errors.New("HEIC preview is unavailable on this server; download the original image")
	}
	if len(data) == 0 || len(data) > 20<<20 {
		return nil, errors.New("image exceeds the preview size limit")
	}
	dir, err := os.MkdirTemp("", "openvibely-image-preview-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	input, output := filepath.Join(dir, "input.heic"), filepath.Join(dir, "preview.jpg")
	if err = os.WriteFile(input, data, 0600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/sips", "-s", "format", "jpeg", "-s", "formatOptions", "85", "-Z", "1024", input, "--out", output)
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", windowsImagePreviewScript)
		cmd.Env = append(os.Environ(), "OPENVIBELY_PREVIEW_INPUT="+input, "OPENVIBELY_PREVIEW_OUTPUT="+output)
	}
	if err = cmd.Run(); err != nil {
		return nil, errors.New("image preview conversion failed; download the original image")
	}
	info, err := os.Stat(output)
	if err != nil {
		return nil, err
	}
	if info.Size() > 8<<20 {
		return nil, errors.New("converted preview exceeds size limit")
	}
	return os.ReadFile(output)
}
