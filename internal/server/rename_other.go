//go:build !windows

package server

import "syscall"

const crossDeviceRenameError = syscall.EXDEV
