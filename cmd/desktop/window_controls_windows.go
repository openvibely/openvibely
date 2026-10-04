package main

import (
	"encoding/json"
	"log"
	"sync"
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/sys/windows"
)

var captionUser32 = windows.NewLazySystemDLL("user32.dll")
var captionComctl32 = windows.NewLazySystemDLL("comctl32.dll")
var captionSetSubclass = captionComctl32.NewProc("SetWindowSubclass")
var captionDefSubclass = captionComctl32.NewProc("DefSubclassProc")
var captionRemoveSubclass = captionComctl32.NewProc("RemoveWindowSubclass")
var captionGetClientRect = captionUser32.NewProc("GetClientRect")
var captionScreenToClient = captionUser32.NewProc("ScreenToClient")
var captionTrackMouse = captionUser32.NewProc("TrackMouseEvent")

// Wails' composition-hosted WebView leaves non-client hit testing with the
// top-level HWND. HTMAXBUTTON makes Windows 11 recognise the custom maximize
// button for Snap Layouts; ordinary HTML hover handlers cannot do this.
func registerPlatformWindowControls(app *application.App, window *application.WebviewWindow) {
	var mu sync.RWMutex
	var bounds captionBounds
	app.Event.On("desktop:caption-bounds", func(event *application.CustomEvent) {
		if event.Sender != "main" {
			return
		}
		data, err := json.Marshal(event.Data)
		if err != nil {
			return
		}
		var next captionBounds
		if json.Unmarshal(data, &next) != nil || !next.valid() {
			return
		}
		mu.Lock()
		bounds = next
		mu.Unlock()
	})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		application.InvokeAsync(func() {
			// Wails centers the requested DIP size without clamping it. On a
			// scaled/small display this can place the entire caption offscreen.
			if screen, err := window.GetScreen(); err == nil && screen != nil {
				width, height := window.Size()
				fitWidth, fitHeight := fitDesktopWindowSize(width, height, screen.WorkArea.Width, screen.WorkArea.Height)
				if fitWidth != width || fitHeight != height {
					window.SetMinSize(min(desktopMinWidth, fitWidth), min(desktopMinHeight, fitHeight))
					window.SetSize(fitWidth, fitHeight)
					window.Center()
				}
			}
			hwnd := uintptr(window.NativeWindow())
			if hwnd == 0 {
				return
			}
			hover, pressed := false, false
			notify := func(h, p bool) {
				if h == hover && p == pressed {
					return
				}
				hover, pressed = h, p
				app.Event.Emit("desktop:caption-hover", map[string]bool{"hover": h, "pressed": p})
			}
			var callback uintptr
			callback = syscall.NewCallback(func(hwnd uintptr, msg uint32, wp, lp, id, ref uintptr) uintptr {
				switch msg {
				case 0x0084: // WM_NCHITTEST; lParam contains signed physical screen coordinates.
					point := struct{ X, Y int32 }{int32(int16(lp & 0xffff)), int32(int16((lp >> 16) & 0xffff))}
					var rect struct{ Left, Top, Right, Bottom int32 }
					captionScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&point)))
					captionGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
					mu.RLock()
					b := bounds
					mu.RUnlock()
					if b.contains(float64(point.X), float64(point.Y), float64(rect.Right)) {
						return 9
					} // HTMAXBUTTON
				case 0x00a0: // WM_NCMOUSEMOVE
					if wp == 9 {
						notify(true, pressed)
						track := struct {
							Size, Flags uint32
							Window      uintptr
							HoverTime   uint32
						}{}
						track.Size = uint32(unsafe.Sizeof(track))
						track.Flags = 0x12 // TME_LEAVE | TME_NONCLIENT
						track.Window = hwnd
						captionTrackMouse.Call(uintptr(unsafe.Pointer(&track)))
					} else {
						notify(false, false)
					}
				case 0x02a2, 0x0008: // WM_NCMOUSELEAVE / WM_KILLFOCUS
					notify(false, false)
				case 0x00a1: // WM_NCLBUTTONDOWN
					if wp == 9 {
						notify(true, true)
						return 0
					}
				case 0x00a2: // WM_NCLBUTTONUP
					if wp == 9 && pressed {
						notify(true, false)
						application.InvokeAsync(func() {
							if window.IsFullscreen() {
								window.UnFullscreen()
							} else {
								window.ToggleMaximise()
							}
						})
						return 0
					}
				case 0x0082: // WM_NCDESTROY
					captionRemoveSubclass.Call(hwnd, callback, 1)
				}
				result, _, _ := captionDefSubclass.Call(hwnd, uintptr(msg), wp, lp)
				return result
			})
			if ok, _, err := captionSetSubclass.Call(hwnd, callback, 1, 0); ok == 0 {
				log.Printf("[desktop] native caption hit testing unavailable: %v", err)
			}
		})
	})
}
