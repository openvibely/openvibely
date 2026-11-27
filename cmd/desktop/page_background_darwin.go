package main

/*
#cgo CFLAGS: -x objective-c
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

@protocol OVPageBackgroundHost
- (WKWebView *)webView;
@end

static void ovSetPageBackground(void *nativeWindow, int r, int g, int b) {
    NSWindow<OVPageBackgroundHost> *window = (NSWindow<OVPageBackgroundHost> *)nativeWindow;
    if (!window || ![window respondsToSelector:@selector(webView)]) return;
    WKWebView *view = window.webView;
    NSColor *colour = [NSColor colorWithRed:r/255.0 green:g/255.0 blue:b/255.0 alpha:1.0];
    // Paint inside the content view's existing rounded clip, never behind it.
    window.opaque = NO;
    window.backgroundColor = NSColor.clearColor;
    window.contentView.wantsLayer = YES;
    window.contentView.layer.backgroundColor = colour.CGColor;
    if (@available(macOS 12.0, *)) {
        view.underPageBackgroundColor = colour;
    }
    // WebKit otherwise paints a white document canvas between navigations.
    // Wails' public-API transparency setting is a no-op on macOS.
    @try {
        [view setValue:@NO forKey:@"drawsBackground"];
    } @catch (NSException *exception) {
        // Retain the public under-page colour if this WebKit lacks the switch.
    }
}
*/
import "C"

import (
	"encoding/json"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func registerPageBackground(app *application.App, window *application.WebviewWindow) {
	apply := func(rgb [3]int) {
		application.InvokeAsync(func() { C.ovSetPageBackground(window.NativeWindow(), C.int(rgb[0]), C.int(rgb[1]), C.int(rgb[2])) })
	}
	app.Event.On("desktop:page-background", func(event *application.CustomEvent) {
		if event.Sender != window.Name() {
			return
		}
		data, err := json.Marshal(event.Data)
		if err != nil {
			return
		}
		var rgba []int
		if json.Unmarshal(data, &rgba) != nil || len(rgba) != 4 || rgba[3] != 255 {
			return
		}
		for _, channel := range rgba {
			if channel < 0 || channel > 255 {
				return
			}
		}
		apply([3]int{rgba[0], rgba[1], rgba[2]})
	})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		apply([3]int{21, 25, 30})
	})
}
