//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fblocks
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

static NSWindow *ovFullscreenWindow;
static NSApplicationPresentationOptions ovSavedPresentation;
static BOOL ovPresentationApplied;

static void ovRestorePresentation(void) {
    if (!ovPresentationApplied) return;
    [NSApp setPresentationOptions:ovSavedPresentation];
    ovPresentationApplied = NO;
}

static void ovHideFullscreenChrome(void) {
    if (!ovFullscreenWindow || ![NSApp isActive]) return;
    if (!ovPresentationApplied) ovSavedPresentation = [NSApp presentationOptions];
    // AutoHideToolbar requires AutoHideMenuBar, so remove both along with
    // AutoHideDock before selecting the non-revealing menu/Dock options.
    NSApplicationPresentationOptions options = [NSApp presentationOptions];
    options &= ~(NSApplicationPresentationAutoHideMenuBar |
                 NSApplicationPresentationAutoHideDock |
                 NSApplicationPresentationAutoHideToolbar);
    options |= NSApplicationPresentationHideMenuBar | NSApplicationPresentationHideDock;
    [NSApp setPresentationOptions:options];
    ovPresentationApplied = YES;
}

static void ovInstallFullscreenPresentation(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        NSNotificationCenter *center = [NSNotificationCenter defaultCenter];
        [center addObserverForName:NSWindowDidEnterFullScreenNotification object:nil queue:nil usingBlock:^(NSNotification *note) {
            ovFullscreenWindow = note.object;
            ovHideFullscreenChrome();
        }];
        [center addObserverForName:NSWindowWillExitFullScreenNotification object:nil queue:nil usingBlock:^(NSNotification *note) {
            if (note.object != ovFullscreenWindow) return;
            ovRestorePresentation();
            ovFullscreenWindow = nil;
        }];
        [center addObserverForName:NSWindowWillCloseNotification object:nil queue:nil usingBlock:^(NSNotification *note) {
            if (note.object != ovFullscreenWindow) return;
            ovRestorePresentation();
            ovFullscreenWindow = nil;
        }];
        [center addObserverForName:NSApplicationDidResignActiveNotification object:nil queue:nil usingBlock:^(NSNotification *note) {
            ovRestorePresentation();
        }];
        [center addObserverForName:NSApplicationDidBecomeActiveNotification object:nil queue:nil usingBlock:^(NSNotification *note) {
            ovHideFullscreenChrome();
        }];
    });
}
*/
import "C"

// Called on AppKit's main thread after application startup. Observers live for
// the application lifetime; presentation changes never modify system settings.
func installFullscreenPresentation() {
	C.ovInstallFullscreenPresentation()
}
