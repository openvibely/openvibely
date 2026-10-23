//go:build openvibely_native_ui

package main

import "testing"

func TestNativeWheelHitTestCoordinates(t *testing.T) {
	if !checkNativeWheelCoordinates() {
		t.Fatal("wheel routing must hit the intended child inside an offset, flipped view")
	}
}

func TestNativeWheelScreenCoordinates(t *testing.T) {
	if !checkNativeWheelScreenCoordinates() {
		t.Fatal("CoreGraphics and NSEvent must preserve the requested screen point and wheel payload")
	}
}
