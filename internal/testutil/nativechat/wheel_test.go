//go:build openvibely_native_ui

package main

import (
	"strings"
	"testing"
)

func TestNativeWheelRejectsMissingWindow(t *testing.T) {
	// Missing native targets must fail dispatch, not acknowledge an input the
	// browser can never receive and leave calibration waiting for its timeout.
	err := nativeWheel(nil, wheel{X: 6, Y: 75, DY: 40, Scale: 1, phase: 1})
	if err == nil || !strings.Contains(err.Error(), "window") {
		t.Fatalf("expected missing-window routing error, got %v", err)
	}
}
