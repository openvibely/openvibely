//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation
#import <Foundation/Foundation.h>

static void ovSetDesktopProcessName(void) {
    @autoreleasepool {
        // Bundled launches use CFBundleName from Info.plist. Direct executable
        // launches use the process name as AppKit's application-name fallback.
        [[NSProcessInfo processInfo] setProcessName:@"OpenVibely"];
    }
}
*/
import "C"

// Run before starting the backend or initializing AppKit: changing the process
// name is not thread-safe and AppKit may cache it during initialization.
func configureDesktopAppName() {
	C.ovSetDesktopProcessName()
}
