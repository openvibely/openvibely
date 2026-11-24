//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// Wails exposes hidden items but not AppKit's opt-in to their shortcuts.
// Called on the main thread after Wails has installed the application menu.
static void ovEnableHiddenMenuShortcuts(NSMenu *menu) {
    for (NSMenuItem *item in menu.itemArray) {
        if (item.hidden) item.allowsKeyEquivalentWhenHidden = YES;
        if (item.submenu) ovEnableHiddenMenuShortcuts(item.submenu);
    }
}
static void ovConfigureMenuShortcuts(void) {
    ovEnableHiddenMenuShortcuts(NSApp.mainMenu);
}
*/
import "C"

func enableHiddenMenuShortcuts() {
	C.ovConfigureMenuShortcuts()
}
