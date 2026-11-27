package main

/*
#cgo CFLAGS: -x objective-c -fblocks
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>

@protocol OVLoginWebViewHost
- (WKWebView *)webView;
@end

static char ovLoginCoverKey;

static void ovRemoveLoginCover(void *nativeWindow) {
    NSWindow *window = (NSWindow *)nativeWindow;
    NSImageView *cover = objc_getAssociatedObject(window, &ovLoginCoverKey);
    [cover removeFromSuperview];
    objc_setAssociatedObject(window, &ovLoginCoverKey, nil, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
}

// Acknowledge removal in the destination document so its visible timer starts here.
static void ovRevealLoginDestination(void *nativeWindow) {
    NSWindow<OVLoginWebViewHost> *window = (NSWindow<OVLoginWebViewHost> *)nativeWindow;
    ovRemoveLoginCover(window);
    if ([window respondsToSelector:@selector(webView)]) {
        [window.webView evaluateJavaScript:@"document.dispatchEvent(new Event('ov-desktop-revealed'));" completionHandler:nil];
    }
}

static void ovNavigateFromLogin(void *nativeWindow, const char *navigationScript) {
    NSWindow<OVLoginWebViewHost> *window = (NSWindow<OVLoginWebViewHost> *)nativeWindow;
    if (!window || ![window respondsToSelector:@selector(webView)]) return;
    WKWebView *view = window.webView;
    NSString *script = [NSString stringWithUTF8String:navigationScript];
    // Capture the existing sign-in screen before starting the navigation.
    // The image stays only in memory and inside the existing rounded clip.
    [view takeSnapshotWithConfiguration:nil completionHandler:^(NSImage *image, NSError *error) {
        ovRemoveLoginCover(window);
        NSImageView *cover = nil;
        if (image && !error) {
            cover = [[[NSImageView alloc] initWithFrame:[view convertRect:view.bounds toView:window.contentView]] autorelease];
            cover.image = image;
            cover.imageScaling = NSImageScaleAxesIndependently;
            cover.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
            [window.contentView addSubview:cover positioned:NSWindowAbove relativeTo:view];
            objc_setAssociatedObject(window, &ovLoginCoverKey, cover, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
            // Never leave a failed or unsupported destination hidden indefinitely.
            dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 15 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
                if (objc_getAssociatedObject(window, &ovLoginCoverKey) == cover) ovRemoveLoginCover(window);
            });
        }
        [view evaluateJavaScript:script completionHandler:^(id result, NSError *failure) {
            if (failure) ovRemoveLoginCover(window);
        }];
    }];
}
*/
import "C"

import (
	"encoding/json"
	"strings"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func registerLoginTransition(app *application.App, window *application.WebviewWindow) {
	app.Event.On("desktop:login-navigate", func(event *application.CustomEvent) {
		if event.Sender != window.Name() {
			return
		}
		destination, ok := event.Data.(string)
		// Login destinations must remain on the app origin, just like DecodeNext.
		if !ok || !strings.HasPrefix(destination, "/") || strings.HasPrefix(destination, "//") || strings.ContainsAny(destination, "\\\r\n") {
			return
		}
		encoded, _ := json.Marshal(destination)
		script := "window.location.replace(" + string(encoded) + ");"
		application.InvokeAsync(func() {
			cscript := C.CString(script)
			defer C.free(unsafe.Pointer(cscript))
			C.ovNavigateFromLogin(window.NativeWindow(), cscript)
		})
	})
	app.Event.On("desktop:login-page-ready", func(event *application.CustomEvent) {
		if event.Sender != window.Name() {
			return
		}
		application.InvokeAsync(func() { C.ovRevealLoginDestination(window.NativeWindow()) })
	})
}
