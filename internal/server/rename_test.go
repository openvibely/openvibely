package server

import (
	"fmt"
	"os"
	"testing"
)

func TestCrossDeviceRenameErrorCode(t *testing.T) {
	for _, err := range []error{
		crossDeviceRenameError,
		&os.LinkError{Op: "rename", Old: "source", New: "destination", Err: crossDeviceRenameError},
		fmt.Errorf("migration failed: %w", &os.LinkError{Op: "rename", Old: "source", New: "destination", Err: crossDeviceRenameError}),
	} {
		if !isCrossDeviceRename(err) {
			t.Errorf("cross-device error not recognized: %v", err)
		}
	}
	for _, err := range []error{
		nil,
		&os.LinkError{Op: "rename", Old: "source", New: "destination", Err: os.ErrPermission},
		&os.LinkError{Op: "rename", Old: "source", New: "destination", Err: os.ErrNotExist},
	} {
		if isCrossDeviceRename(err) {
			t.Errorf("unrelated error classified as cross-device: %v", err)
		}
	}
}
