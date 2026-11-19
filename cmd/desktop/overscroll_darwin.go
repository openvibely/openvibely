package main

/*
#cgo CFLAGS: -x objective-c
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

// Wails exposes its main WKWebView through the native window's webView getter.
@protocol OVWebViewHost
- (WKWebView *)webView;
@end

@interface WKWebView (OVRubberBanding)
- (NSUInteger)_rubberBandingEnabled;
- (void)_setRubberBandingEnabled:(NSUInteger)edges;
@end

static void ovDisableVerticalOverscroll(void *nativeWindow) {
    NSWindow<OVWebViewHost> *window = (NSWindow<OVWebViewHost> *)nativeWindow;
    if (![window respondsToSelector:@selector(webView)]) return;
    WKWebView *view = window.webView;
    // Some macOS WebKit releases bounce the viewport despite the shell's CSS.
    // Guard WebKit's private edge mask and leave inner scroll views alone.
    if (![view respondsToSelector:@selector(_rubberBandingEnabled)] ||
        ![view respondsToSelector:@selector(_setRubberBandingEnabled:)]) return;
    NSUInteger vertical = (1UL << CGRectMinYEdge) | (1UL << CGRectMaxYEdge);
    [view _setRubberBandingEnabled:[view _rubberBandingEnabled] & ~vertical];
}
*/
import "C"

import (
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func registerVerticalOverscrollFix(window *application.WebviewWindow) {
	window.OnWindowEvent(events.Mac.WebViewDidCommitNavigation, func(*application.WindowEvent) {
		application.InvokeAsync(func() { C.ovDisableVerticalOverscroll(window.NativeWindow()) })
	})
}
