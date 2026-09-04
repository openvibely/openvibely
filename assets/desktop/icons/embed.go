// Package desktopicons exposes the platform-neutral application icon to the
// desktop runtime. Native packaging uses the sibling ICNS/ICO assets.
package desktopicons

import _ "embed"

// OpenVibelyPNG is the transparent 1024px application icon.
//
//go:embed openvibely.png
var OpenVibelyPNG []byte

// BrowserPNG is the compact icon served by both the server and desktop web UI.
//
//go:embed linux/32x32.png
var BrowserPNG []byte
