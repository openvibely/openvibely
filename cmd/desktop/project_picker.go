package main

import (
	"context"
	"fmt"
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
		if err != nil {
			return "", false, err
		}
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		return path, path == "", nil
	}
}
