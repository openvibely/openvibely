package main

/*
#cgo gtk3 pkg-config: gtk+-3.0
#cgo !gtk3 pkg-config: gtk4
#include "window_controls_linux.h"
*/
import "C"

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"sync"
)

var linuxCaptionState struct {
	sync.Mutex
	app         *application.App
	installed   bool
	left, right int
}

//export ovLinuxCaptionLayout
func ovLinuxCaptionLayout(left, right C.int) {
	linuxCaptionState.Lock()
	linuxCaptionState.left, linuxCaptionState.right = int(left), int(right)
	linuxCaptionState.installed = true
	app := linuxCaptionState.app
	linuxCaptionState.Unlock()
	if app != nil {
		app.Event.Emit("desktop:linux-controls", map[string]int{"left": int(left), "right": int(right)})
	}
}

func registerPlatformWindowControls(app *application.App, window *application.WebviewWindow) {
	linuxCaptionState.Lock()
	linuxCaptionState.app = app
	linuxCaptionState.Unlock()
	app.Event.On("desktop:controls-theme", func(event *application.CustomEvent) {
		if event.Sender != "main" {
			return
		}
		data, ok := event.Data.(map[string]any)
		if !ok {
			return
		}
		dark, ok := data["dark"].(bool)
		if !ok {
			return
		}
		application.InvokeAsync(func() {
			value := C.int(0)
			if dark {
				value = 1
			}
			C.ovSetLinuxCaptionDark(value)
		})
	})
	app.Event.On("desktop:controls-ready", func(event *application.CustomEvent) {
		if event.Sender != "main" {
			return
		}
		linuxCaptionState.Lock()
		installed, left, right := linuxCaptionState.installed, linuxCaptionState.left, linuxCaptionState.right
		linuxCaptionState.Unlock()
		if installed {
			app.Event.Emit("desktop:linux-controls", map[string]int{"left": left, "right": right})
		}
	})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		application.InvokeAsync(func() { C.ovInstallLinuxCaption(window.NativeWindow()) })
	})
}
