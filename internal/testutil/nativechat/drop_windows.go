//go:build openvibely_native_ui

package main

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed testdata/file_drop_windows.ps1
var windowsDropSource []byte

func nativeFileDrop(ctx context.Context, window *application.WebviewWindow, e fileDrop, path string) error {
	var target point
	var positionErr error
	application.InvokeSync(func() {
		native := window.NativeWindow()
		if native == nil {
			positionErr = fmt.Errorf("native drop window missing")
			return
		}
		target = point{int32(e.X * e.Scale), int32(e.Y * e.Scale)}
		if ok, _, err := clientToScreen.Call(uintptr(native), uintptr(unsafe.Pointer(&target))); ok == 0 {
			positionErr = fmt.Errorf("drop coordinates: %v", err)
		}
	})
	if positionErr != nil {
		return positionErr
	}
	dir, err := os.MkdirTemp("", "ov-native-drop-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "source.ps1")
	if err := os.WriteFile(script, windowsDropSource, 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass", "-File", script,
		"-FilePath", path, "-TargetX", strconv.Itoa(int(target.X)), "-TargetY", strconv.Itoa(int(target.Y)))
	cmd.WaitDelay = 2 * time.Second
	getCursorPos := user32.NewProc("GetCursorPos")
	setCursorPos := user32.NewProc("SetCursorPos")
	mouseEvent := user32.NewProc("mouse_event")
	var original point
	if ok, _, err := getCursorPos.Call(uintptr(unsafe.Pointer(&original))); ok == 0 {
		return fmt.Errorf("capture cursor before native drag: %v", err)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start native OLE drag: %w", err)
	}
	// CommandContext can kill PowerShell before its finally block runs. Release
	// the button and restore the cursor in the parent after the helper exits,
	// including when Wait reports cancellation or a timeout.
	defer func() {
		mouseEvent.Call(0x0004, 0, 0, 0, 0) // MOUSEEVENTF_LEFTUP
		setCursorPos.Call(uintptr(original.X), uintptr(original.Y))
	}()
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("native OLE drag: %w: %s", err, output.String())
	}
	return nil
}
