// Package desktopicons exposes the platform-neutral application icon to the
// desktop runtime. Native packaging uses the sibling ICNS/ICO assets.
package desktopicons

import "embed"

// OpenVibelyPNG is the transparent 1024px application icon.
//
//go:embed openvibely.png
var OpenVibelyPNG []byte

// BrowserPNG is the compact icon served by both the server and desktop web UI.
// Browser assets use a centered 924px square crop of openvibely.png to remove
// desktop padding before resizing, preserving the original artwork.
//
//go:embed browser.png
var BrowserPNG []byte

// BrowserICO contains the same cropped artwork at 16, 32, and 48px.
//
//go:embed browser.ico
var BrowserICO []byte

// LinuxIconFiles contains the freedesktop hicolor application icon sizes.
//
//go:embed linux/*.png
var LinuxIconFiles embed.FS
