//go:build !windows && !linux && !darwin

package main

import "github.com/wailsapp/wails/v3/pkg/application"

func registerPlatformWindowControls(_ *application.App, _ *application.WebviewWindow) {}
