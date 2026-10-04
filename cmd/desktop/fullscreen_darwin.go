//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fblocks
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

// AppKit asks the window delegate for these options during the transition.
// Keep system menu/Dock edge reveal without requesting Wails' toolbar reveal.
static NSApplicationPresentationOptions ovFullscreenOptions(NSApplicationPresentationOptions options) {
    options &= ~(NSApplicationPresentationHideMenuBar |
                 NSApplicationPresentationHideDock |
                 NSApplicationPresentationAutoHideToolbar);
    return options | NSApplicationPresentationAutoHideMenuBar | NSApplicationPresentationAutoHideDock;
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

static void ovApplyFullscreenPresentation(void) {
    if (!ovFullscreenWindow || ![NSApp isActive]) return;
    if (!ovPresentationApplied) ovSavedPresentation = [NSApp presentationOptions];
    // Preserve menu bar and Dock edge reveal when returning to the app.
    [NSApp setPresentationOptions:ovFullscreenOptions([NSApp presentationOptions])];
    ovPresentationApplied = YES;
}

// AppKit creates a themed fullscreen frame even for borderless windows.
// Chromium uses _titlebarHeight to suppress its native reveal strip:
// components/remote_cocoa/app_shim/native_widget_mac_frameless_nswindow.mm.
// This is a private AppKit hook: fall back to the native frame if unavailable.
// Add the frame factory override to Wails' class; never change the class of a
// live NSWindow (AppKit keeps class-dependent state for existing windows).
static IMP ovOriginalFrameClass;
static CGFloat ovZeroTitlebarHeight(id self, SEL selector) { return 0; }
static Class ovFramelessFrameClass(id self, SEL selector, NSUInteger style) {
    Class frame = ((Class (*)(id, SEL, NSUInteger))ovOriginalFrameClass)(self, selector, style);
    if (!(style & NSWindowStyleMaskFullScreen) || (style & NSWindowStyleMaskTitled)) return frame;
    SEL heightSelector = sel_registerName("_titlebarHeight");
    Method height = class_getInstanceMethod(frame, heightSelector);
    if (!height) return frame;
    NSString *name = [@"OVFramelessFrame_" stringByAppendingString:NSStringFromClass(frame)];
    Class subclass = NSClassFromString(name);
    if (!subclass) {
        subclass = objc_allocateClassPair(frame, name.UTF8String, 0);
        if (!subclass || !class_addMethod(subclass, heightSelector, (IMP)ovZeroTitlebarHeight, method_getTypeEncoding(height))) {
            if (subclass) objc_disposeClassPair(subclass);
            return frame;
        }
        objc_registerClassPair(subclass);
    }
    return subclass;
}
static void ovInstallFramelessFrame(void) {
    Class windowClass = NSClassFromString(@"WebviewWindow");
    SEL selector = sel_registerName("frameViewClassForStyleMask:");
    Method method = class_getClassMethod(windowClass, selector);
    if (!method) return;
    ovOriginalFrameClass = method_getImplementation(method);
    if (!class_addMethod(object_getClass(windowClass), selector, (IMP)ovFramelessFrameClass, method_getTypeEncoding(method))) {
        NSLog(@"OpenVibely: could not install fullscreen frame override");
    }
}

static void ovInstallFullscreenPresentation(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        ovInstallFramelessFrame();
        NSNotificationCenter *center = [NSNotificationCenter defaultCenter];
        for (NSWindow *window in [NSApp windows]) ovConfigureFullscreenDelegate(window);
        [center addObserverForName:NSWindowDidBecomeKeyNotification object:nil queue:nil usingBlock:^(NSNotification *note) {
            ovConfigureFullscreenDelegate(note.object);
        }];
        [center addObserverForName:NSWindowDidEnterFullScreenNotification object:nil queue:nil usingBlock:^(NSNotification *note) {
            ovFullscreenWindow = note.object;
            ovApplyFullscreenPresentation();
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
            ovApplyFullscreenPresentation();
        }];
    });
}
*/
import "C"

// Called on AppKit's main thread after application startup. Observers live for
// the application lifetime; presentation changes never modify system settings.
func installFullscreenPresentation() {
	C.ovInstallFullscreenPresentation()
	installNativeTrafficLights()
}
