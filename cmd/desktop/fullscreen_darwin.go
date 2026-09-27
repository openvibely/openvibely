//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fblocks
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

// AppKit asks the window delegate for these options during the transition.
// Setting NSApp.presentationOptions only after entry leaves Wails' native
// AutoHideToolbar policy in place, allowing chrome to reveal on edge hover.
static NSApplicationPresentationOptions ovFullscreenOptions(NSApplicationPresentationOptions options) {
    options &= ~(NSApplicationPresentationAutoHideMenuBar |
                 NSApplicationPresentationAutoHideDock |
                 NSApplicationPresentationAutoHideToolbar);
    return options | NSApplicationPresentationHideMenuBar | NSApplicationPresentationHideDock;
}

static NSApplicationPresentationOptions ovWindowFullscreenOptions(id self, SEL selector, NSWindow *window, NSApplicationPresentationOptions proposed) {
    return ovFullscreenOptions(proposed);
}

static void ovConfigureFullscreenDelegate(NSWindow *window) {
    id delegate = window.delegate;
    SEL selector = @selector(window:willUseFullScreenPresentationOptions:);
    if (!delegate || ![delegate respondsToSelector:selector]) return;
    Class original = object_getClass(delegate);
    NSString *name = NSStringFromClass(original);
    if ([name hasPrefix:@"OVFullscreen_"]) return;
    // Subclass this delegate instance rather than replacing Wails' delegate or
    // globally swizzling its class. All other callbacks retain Wails' behavior.
    NSString *subclassName = [@"OVFullscreen_" stringByAppendingString:name];
    Class subclass = NSClassFromString(subclassName);
    if (!subclass) {
        subclass = objc_allocateClassPair(original, subclassName.UTF8String, 0);
        Method method = class_getInstanceMethod(original, selector);
        if (!subclass || !class_addMethod(subclass, selector, (IMP)ovWindowFullscreenOptions, method_getTypeEncoding(method))) {
            if (subclass) objc_disposeClassPair(subclass);
            NSLog(@"OpenVibely: could not install fullscreen presentation delegate");
            return;
        }
        objc_registerClassPair(subclass);
    }
    object_setClass(delegate, subclass);
}

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
    [NSApp setPresentationOptions:ovFullscreenOptions([NSApp presentationOptions])];
    ovPresentationApplied = YES;
}

static void ovInstallFullscreenPresentation(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        NSNotificationCenter *center = [NSNotificationCenter defaultCenter];
        for (NSWindow *window in [NSApp windows]) ovConfigureFullscreenDelegate(window);
        [center addObserverForName:NSWindowDidBecomeKeyNotification object:nil queue:nil usingBlock:^(NSNotification *note) {
            ovConfigureFullscreenDelegate(note.object);
        }];
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
