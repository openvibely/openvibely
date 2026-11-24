//go:build !darwin

package main

// WebView editing shortcuts on Windows/Linux do not require native menu items.
func enableHiddenMenuShortcuts() {}
