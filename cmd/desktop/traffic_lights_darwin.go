//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fblocks
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

// AppKit's standard window buttons use this callback to refresh their cached
// group-hover artwork. NSButtonCell mouseEntered: is for ordinary push buttons.
@interface NSButton (OVWindowButtonHover)
- (void)mouseEnteredOrExited;
@end

// Native controls live above the webview, in the space reserved by the HTML
// header. AppKit draws their artwork, including fullscreen and disabled states.
@interface OVTrafficLights : NSView {
    NSTrackingArea *_hoverArea;
}
- (void)refresh;
@end

@implementation OVTrafficLights
- (instancetype)init {
    self = [super initWithFrame:NSZeroRect];
    if (!self) return nil;
    NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
        NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
    NSArray *types = @[@(NSWindowCloseButton), @(NSWindowMiniaturizeButton), @(NSWindowZoomButton)];
    SEL actions[] = {@selector(closeWindow:), @selector(minimiseWindow:), @selector(fullscreenWindow:)};
    CGFloat x = 0, height = 0;
    for (NSUInteger i = 0; i < types.count; i++) {
        NSButton *button = [NSWindow standardWindowButton:[types[i] integerValue] forStyleMask:style];
        button.target = self;
        button.action = actions[i];
        [button setFrameOrigin:NSMakePoint(x, 0)];
        [self addSubview:button];
        x += NSWidth(button.frame) + 9;
        height = MAX(height, NSHeight(button.frame));
    }
    [self setFrameSize:NSMakeSize(x - 9, height)];
    self.autoresizingMask = NSViewMinYMargin | NSViewMaxXMargin;
    return self;
}
- (void)dealloc {
    [_hoverArea release];
    [super dealloc];
}
- (void)updateTrackingAreas {
    [super updateTrackingAreas];
    if (_hoverArea) { [self removeTrackingArea:_hoverArea]; [_hoverArea release]; }
    _hoverArea = [[NSTrackingArea alloc] initWithRect:NSZeroRect
        options:NSTrackingMouseEnteredAndExited | NSTrackingMouseMoved | NSTrackingActiveAlways | NSTrackingInVisibleRect
        owner:self userInfo:nil];
    [self addTrackingArea:_hoverArea];
    [self refresh];
}
// Fullscreen transitions can replace tracking areas without delivering an exit.
// Read the pointer position when AppKit draws, rather than retaining a stale
// mouse-entered flag across a resize, focus change, or fullscreen transition.
- (BOOL)_mouseInGroup:(NSButton *)button {
    if (!self.window.isKeyWindow || self.hiddenOrHasHiddenAncestor) return NO;
    NSPoint point = [self convertPoint:self.window.mouseLocationOutsideOfEventStream fromView:nil];
    return NSMouseInRect(point, self.bounds, self.isFlipped);
}
- (void)mouseEntered:(NSEvent *)event { [self refresh]; }
- (void)mouseExited:(NSEvent *)event { [self refresh]; }
- (void)mouseMoved:(NSEvent *)event { [self refresh]; }
- (NSView *)hitTest:(NSPoint)point {
    NSView *hit = [super hitTest:point];
    return hit == self ? nil : hit;
}
- (void)refresh {
    BOOL fullscreen = (self.window.styleMask & NSWindowStyleMaskFullScreen) != 0;
    for (NSButton *button in self.subviews) {
        button.enabled = button.action != @selector(minimiseWindow:) || !fullscreen;
        if ([button respondsToSelector:@selector(mouseEnteredOrExited)]) {
            [button mouseEnteredOrExited];
        }
        [button setNeedsDisplay:YES];
    }
}
- (void)closeWindow:(id)sender {
    NSWindow *window = self.window;
    // Preserve Wails' WindowClosing event and cancellation handling.
    if (![window.delegate respondsToSelector:@selector(windowShouldClose:)] ||
        [window.delegate windowShouldClose:window]) [window close];
}
- (void)minimiseWindow:(id)sender {
    if (!(self.window.styleMask & NSWindowStyleMaskFullScreen)) [self.window miniaturize:sender];
}
- (void)fullscreenWindow:(id)sender { [self.window toggleFullScreen:sender]; }
@end

static char ovTrafficLightsKey;
static NSAppearanceName ovTrafficLightsAppearanceName;
static void ovSetTrafficLightsDark(int dark) {
    ovTrafficLightsAppearanceName = dark ? NSAppearanceNameDarkAqua : NSAppearanceNameAqua;
    for (NSWindow *window in NSApp.windows) {
        OVTrafficLights *controls = objc_getAssociatedObject(window, &ovTrafficLightsKey);
        controls.appearance = [NSAppearance appearanceNamed:ovTrafficLightsAppearanceName];
        [controls refresh];
    }
}
static void ovAttachTrafficLights(NSWindow *window) {
    if (![window isKindOfClass:NSClassFromString(@"WebviewWindow")]) return;
    NSView *content = window.contentView;
    if (!content) return;
    OVTrafficLights *controls = objc_getAssociatedObject(window, &ovTrafficLightsKey);
    if (!controls) {
        controls = [[OVTrafficLights alloc] init];
        objc_setAssociatedObject(window, &ovTrafficLightsKey, controls, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
        [controls release];
    }
    if (controls.superview != content) [content addSubview:controls positioned:NSWindowAbove relativeTo:nil];
    if (ovTrafficLightsAppearanceName) controls.appearance = [NSAppearance appearanceNamed:ovTrafficLightsAppearanceName];
    CGFloat y = content.isFlipped ? (46 - NSHeight(controls.frame)) / 2 :
        NSHeight(content.bounds) - (46 + NSHeight(controls.frame)) / 2;
    [controls setFrameOrigin:NSMakePoint(16, y)];
    [controls refresh];
}
static void ovInstallTrafficLights(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        for (NSWindow *window in NSApp.windows) ovAttachTrafficLights(window);
        NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
        for (NSNotificationName name in @[NSWindowDidBecomeKeyNotification, NSWindowDidResignKeyNotification,
            NSWindowDidResizeNotification, NSWindowDidEnterFullScreenNotification, NSWindowDidExitFullScreenNotification]) {
            [center addObserverForName:name object:nil queue:nil usingBlock:^(NSNotification *note) {
                ovAttachTrafficLights(note.object);
            }];
        }
    });
}
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

func registerPlatformWindowControls(app *application.App, _ *application.WebviewWindow) {
	app.Event.On("desktop:controls-theme", func(event *application.CustomEvent) {
		if event.Sender != "main" {
			return
		}
		data, ok := event.Data.(map[string]any)
		if !ok {
			return
		}
		dark, ok := data["dark"].(bool)
		if !ok {
			return
		}
		application.InvokeAsync(func() {
			value := C.int(0)
			if dark {
				value = 1
			}
			C.ovSetTrafficLightsDark(value)
		})
	})
}

func installNativeTrafficLights() {
	C.ovInstallTrafficLights()
}
