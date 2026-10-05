package main

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"github.com/openvibely/openvibely/internal/handler"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func desktopProjectFolderPicker() handler.ProjectFolderPicker {
	var picking sync.Mutex
	return func(ctx context.Context) (string, bool, error) {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		if !picking.TryLock() {
			return "", false, fmt.Errorf("a folder picker is already open")
		}
		defer picking.Unlock()
		app := application.Get()
		if app == nil {
			return "", false, fmt.Errorf("desktop folder picker is not ready")
		}
		window, ok := app.Window.GetByName("main")
		if !ok {
			return "", false, fmt.Errorf("desktop window is unavailable")
		}
		path, err := app.Dialog.OpenFile().SetTitle("Select Project Repository Folder").CanChooseFiles(false).CanChooseDirectories(true).AttachToWindow(window).PromptForSingleSelection()
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		return desktopFolderSelection(runtime.GOOS, path, err)
	}
}

func desktopFolderSelection(goos, path string, err error) (string, bool, error) {
	// Wails beta.26 returns an internal cfd.ErrorCancelled on Windows.
	// That sentinel cannot be imported; match its exact message only on Windows.
	if goos == "windows" && err != nil && err.Error() == "cancelled by user" {
		return "", true, nil
	}
	if err != nil {
		return "", false, err
	}
	return path, path == "", nil
}
