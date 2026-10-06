//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fblocks
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>

static void ovJavaScriptConfirm(id self, SEL selector, WKWebView *webView,
    NSString *message, WKFrameInfo *frame, void (^completionHandler)(BOOL)) {
    NSWindow *window = webView.window;
    if (!window) {
        completionHandler(NO);
        return;
    }
    NSAlert *alert = [[NSAlert alloc] init];
    alert.messageText = @"Confirm action";
    alert.informativeText = message ?: @"";
    [alert addButtonWithTitle:@"OK"];
    [alert addButtonWithTitle:@"Cancel"];
    [alert beginSheetModalForWindow:window completionHandler:^(NSModalResponse response) {
        completionHandler(response == NSAlertFirstButtonReturn);
    }];
    [alert release];
}

static void ovInstallConfirmationDialogs(void) {
    Class delegate = NSClassFromString(@"WebviewWindowDelegate");
    SEL selector = @selector(webView:runJavaScriptConfirmPanelWithMessage:initiatedByFrame:completionHandler:);
    // Install before Wails assigns WKWebView.UIDelegate: WebKit caches which
    // optional delegate callbacks exist. Preserve any future Wails implementation.
    if (delegate && !class_getInstanceMethod(delegate, selector)) {
        struct objc_method_description method = protocol_getMethodDescription(
            @protocol(WKUIDelegate), selector, NO, YES);
        if (!method.types || !class_addMethod(delegate, selector, (IMP)ovJavaScriptConfirm, method.types)) {
            NSLog(@"OpenVibely: could not install JavaScript confirmation delegate");
        }
    }
}
*/
import "C"

func installNativeConfirmationDialogs() {
	C.ovInstallConfirmationDialogs()
}
