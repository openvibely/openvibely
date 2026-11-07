//go:build openvibely_native_ui

package main

import (
	"context"
	"fmt"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func nativeFileDrop(context.Context, *application.WebviewWindow, fileDrop, string) error {
	return fmt.Errorf("Linux native file drops use TestNativeLinuxComposerPNGDrop with an isolated Xvfb display")
}
