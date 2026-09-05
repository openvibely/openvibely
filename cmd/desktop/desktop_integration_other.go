//go:build !linux

package main

func handleDesktopIntegrationCommand(_ []string) (bool, error) {
	return false, nil
}
