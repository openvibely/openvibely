//go:build openvibely_native_ui

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var user32 = syscall.NewLazyDLL("user32.dll")
var clientToScreen = user32.NewProc("ClientToScreen")
var sendMessage = user32.NewProc("SendMessageW")

type point struct{ X, Y int32 }

func nativeWheel(window unsafe.Pointer, e wheel) error {
	if window == nil {
		return fmt.Errorf("native wheel routing failed: window missing")
	}
	scale := e.Scale
	if scale <= 0 {
		scale = 1
	}
	p := point{int32(e.X * scale), int32(e.Y * scale)}
	if ok, _, err := clientToScreen.Call(uintptr(window), uintptr(unsafe.Pointer(&p))); ok == 0 {
		return fmt.Errorf("native wheel coordinate conversion failed: %v", err)
	}
	// Composition-hosted WebView2 receives input through the Wails HWND.
	// Do not depend on whichever window happens to be under a screen point.
	target := uintptr(window)
	coords := uintptr(uint16(p.X)) | uintptr(uint16(p.Y))<<16
	// WebView2 receives real Windows wheel messages. Its configured line/pixel
	// conversion is measured from the resulting trusted DOM event by the fixture.
	if e.DY != 0 {
		sendMessage.Call(target, 0x020A, uintptr(uint16(int16(-e.DY)))<<16, coords)
	}
	if e.DX != 0 {
		sendMessage.Call(target, 0x020E, uintptr(uint16(int16(e.DX)))<<16, coords)
	}
	return nil
}
