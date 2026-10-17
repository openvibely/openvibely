package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/openvibely/openvibely/internal/handler"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func desktopFileSaver() handler.DesktopFileSaver {
	var saving sync.Mutex
	return func(ctx context.Context, name string, source io.Reader) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !saving.TryLock() {
			return false, fmt.Errorf("a save dialog is already open")
		}
		defer saving.Unlock()
		app := application.Get()
		if app == nil {
			return false, fmt.Errorf("desktop is unavailable")
		}
		window, ok := app.Window.GetByName("main")
		if !ok {
			return false, fmt.Errorf("desktop window is unavailable")
		}
		path, err := app.Dialog.SaveFile().SetFilename(name).CanCreateDirectories(true).AttachToWindow(window).PromptForSingleSelection()
		path, canceled, err := desktopFolderSelection(runtime.GOOS, path, err)
		if err != nil || canceled {
			return canceled, err
		}
		return false, saveOriginalFile(ctx, path, source)
	}
}

// Stage beside the destination so a failed transfer leaves an existing file intact.
func saveOriginalFile(ctx context.Context, destination string, source io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".openvibely-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = io.Copy(file, saveContextReader{ctx, source}); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(file.Name(), destination)
}

type saveContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r saveContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}
