//go:build darwin

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeTrafficLightsHoverFollowsPointer(t *testing.T) {
	source, err := os.ReadFile("traffic_lights_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the production Objective-C view in a separate AppKit process.
	// No window is displayed and no system pointer events are synthesized.
	text := string(source)
	start := strings.Index(text, "#import <Cocoa/Cocoa.h>")
	end := strings.Index(text, "*/")
	if start < 0 || end < start {
		t.Fatal("missing native implementation")
	}
	harness := text[start:end] + `
@interface OVHoverTestWindow : NSWindow
@property NSPoint testPointer;
@property BOOL testKey;
@property BOOL testFullscreen;
@end
@implementation OVHoverTestWindow
- (BOOL)isKeyWindow { return self.testKey; }
- (NSWindowStyleMask)styleMask { return [super styleMask] | (self.testFullscreen ? NSWindowStyleMaskFullScreen : 0); }
- (BOOL)isMainWindow { return self.testKey; }
- (BOOL)canBecomeKeyWindow { return YES; }
- (BOOL)canBecomeMainWindow { return YES; }
- (NSPoint)mouseLocationOutsideOfEventStream { return self.testPointer; }
@end
static NSData *pixels(NSView *view) {
 NSBitmapImageRep *rep = [view bitmapImageRepForCachingDisplayInRect:view.bounds];
 [view cacheDisplayInRect:view.bounds toBitmapImageRep:rep];
 return [NSData dataWithBytes:rep.bitmapData length:rep.bytesPerRow * rep.pixelsHigh];
}
int main(void) {
 @autoreleasepool {
  [NSApplication sharedApplication];
  OVHoverTestWindow *window = [[OVHoverTestWindow alloc] initWithContentRect:NSMakeRect(0,0,400,200) styleMask:NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable backing:NSBackingStoreBuffered defer:NO];
  OVTrafficLights *controls = [[OVTrafficLights alloc] init];
  [window.contentView addSubview:controls];
  [controls setFrameOrigin:NSMakePoint(16,160)];
  NSButton *button = controls.subviews[0];
  window.testKey = YES;
  window.testPointer = [controls convertPoint:NSMakePoint(7,7) toView:nil];
  if (![controls _mouseInGroup:button]) return 1;
  [controls mouseEntered:nil];
  // Moving away without an exit event must still clear the hover artwork.
  window.testPointer = NSMakePoint(300,50);
  if ([controls _mouseInGroup:button]) return 2;
  [controls updateTrackingAreas];
  if ([controls _mouseInGroup:button]) return 3;
  window.testPointer = [controls convertPoint:NSMakePoint(7,7) toView:nil];
  if (![controls _mouseInGroup:button]) return 4;
  window.testKey = NO;
  if ([controls _mouseInGroup:button]) return 5;
  window.testKey = YES;
  controls.hidden = YES;
  if ([controls _mouseInGroup:button]) return 6;
  controls.hidden = NO;
  window.testPointer = NSMakePoint(300,50);
  [controls mouseExited:nil];
  NSMutableArray *idle = [NSMutableArray array];
  for (NSButton *sibling in controls.subviews) [idle addObject:pixels(sibling)];
  // Hover each button, including movement between siblings without a group
  // exit: every enabled button must render its hover symbol together.
  for (NSButton *hovered in controls.subviews) {
   window.testPointer = [hovered convertPoint:NSMakePoint(7,7) toView:nil];
   [controls mouseMoved:nil];
   for (NSUInteger i=0; i<controls.subviews.count; i++) {
    if ([idle[i] isEqualToData:pixels(controls.subviews[i])]) return 7;
   }
  }
  window.testPointer = NSMakePoint(300,50);
  [controls mouseExited:nil];
  for (NSUInteger i=0; i<controls.subviews.count; i++) {
   if (![idle[i] isEqualToData:pixels(controls.subviews[i])]) return 8;
  }
  // Fullscreen must leave Minimize disabled with identical idle/hover artwork.
  window.testFullscreen = YES;
  [controls refresh];
  if ([(NSButton *)controls.subviews[1] isEnabled]) return 9;
  NSData *disabled = pixels(controls.subviews[1]);
  window.testPointer = [controls convertPoint:NSMakePoint(7,7) toView:nil];
  [controls mouseEntered:nil];
  if (![disabled isEqualToData:pixels(controls.subviews[1])]) return 10;
  window.testFullscreen = NO;
  [controls refresh];
  if (![(NSButton *)controls.subviews[1] isEnabled]) return 11;
  [controls release];
  [window release];
 }
 return 0;
}
`
	dir := t.TempDir()
	file := filepath.Join(dir, "hover.m")
	binary := filepath.Join(dir, "hover")
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "clang", "-fblocks", "-framework", "Cocoa", file, "-o", binary).CombinedOutput(); err != nil {
		t.Fatalf("compile native hover test: %v\n%s", err, output)
	}
	if output, err := exec.CommandContext(ctx, binary).CombinedOutput(); err != nil {
		t.Fatalf("native hover behavior: %v\n%s", err, output)
	}
}
