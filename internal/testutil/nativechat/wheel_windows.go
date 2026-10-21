//go:build openvibely_native_ui

package main

import (
	"syscall"
	"unsafe"
)

var user32 = syscall.NewLazyDLL("user32.dll")
var clientToScreen = user32.NewProc("ClientToScreen")
var windowFromPoint = user32.NewProc("WindowFromPoint")
var sendMessage = user32.NewProc("SendMessageW")

type point struct{ X, Y int32 }

func nativeWheel(window unsafe.Pointer, e wheel) {
	scale := e.Scale
	if scale <= 0 {
		scale = 1
	}
	p := point{int32(e.X * scale), int32(e.Y * scale)}
	clientToScreen.Call(uintptr(window), uintptr(unsafe.Pointer(&p)))
	packed := uintptr(uint32(p.X)) | uintptr(uint32(p.Y))<<32
	target, _, _ := windowFromPoint.Call(packed)
	coords := uintptr(uint16(p.X)) | uintptr(uint16(p.Y))<<16
	// WebView2 receives real Windows wheel messages. Its configured line/pixel
	// conversion is measured from the resulting trusted DOM event by the fixture.
	if e.DY != 0 {
		sendMessage.Call(target, 0x020A, uintptr(uint16(int16(-e.DY)))<<16, coords)
	}
	if e.DX != 0 {
		sendMessage.Call(target, 0x020E, uintptr(uint16(int16(e.DX)))<<16, coords)
	}
}
