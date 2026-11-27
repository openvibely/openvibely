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

func TestNativeLoginCoverPreservesRoundedClip(t *testing.T) {
	source, err := os.ReadFile("login_transition_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start, end := strings.Index(text, "#import <Cocoa/Cocoa.h>"), strings.Index(text, "*/")
	if start < 0 || end < start {
		t.Fatal("missing native implementation")
	}
	harness := text[start:end] + `
@interface OVTransitionTestWindow : NSWindow <OVLoginWebViewHost>
@property (retain) WKWebView *webView;
@end
@implementation OVTransitionTestWindow
@end
static BOOL spinUntil(BOOL (^condition)(void)) {
 NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:5];
 while (!condition() && deadline.timeIntervalSinceNow > 0) {
  [[NSRunLoop currentRunLoop] runUntilDate:[NSDate dateWithTimeIntervalSinceNow:.01]];
 }
 return condition();
}
int main(void) {
 @autoreleasepool {
  [NSApplication sharedApplication];
  OVTransitionTestWindow *window = [[OVTransitionTestWindow alloc] initWithContentRect:NSMakeRect(0,0,400,250) styleMask:NSWindowStyleMaskBorderless backing:NSBackingStoreBuffered defer:NO];
  window.backgroundColor = NSColor.clearColor;
  window.opaque = NO;
  window.contentView.wantsLayer = YES;
  window.contentView.layer.cornerRadius = 20;
  window.contentView.layer.masksToBounds = YES;
  window.webView = [[WKWebView alloc] initWithFrame:window.contentView.bounds];
  [window.contentView addSubview:window.webView];
  [window orderBack:nil];
  [window.webView loadHTMLString:@"<html><body style='background:#191e24;color:white'>Sign in</body></html>" baseURL:nil];
  if (!spinUntil(^BOOL { return !window.webView.loading; })) return 1;
  ovNavigateFromLogin(window, "document.body.innerHTML='Destination';");
  if (!spinUntil(^BOOL { return objc_getAssociatedObject(window, &ovLoginCoverKey) != nil; })) return 2;
  NSImageView *cover = objc_getAssociatedObject(window, &ovLoginCoverKey);
  if (!cover.image || cover.superview != window.contentView) return 3;
  if (window.contentView.layer.cornerRadius != 20 || !window.contentView.layer.masksToBounds) return 4;
  if (window.opaque || window.backgroundColor.alphaComponent != 0) return 5;
  __block BOOL changed = NO;
  [window.webView evaluateJavaScript:@"document.body.innerText" completionHandler:^(id value, NSError *error) {
   changed = !error && [value isEqualToString:@"Destination"];
  }];
  if (!spinUntil(^BOOL { return changed; })) return 6;
  // The old sign-in image still covers the new document until ready is signalled.
  if (objc_getAssociatedObject(window, &ovLoginCoverKey) != cover) return 7;
  ovRemoveLoginCover(window);
  if (objc_getAssociatedObject(window, &ovLoginCoverKey) || cover.superview) return 8;
  [window orderOut:nil];
 }
 return 0;
}
`
	dir := t.TempDir()
	file, binary := filepath.Join(dir, "transition.m"), filepath.Join(dir, "transition")
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "clang", "-fblocks", "-framework", "Cocoa", "-framework", "WebKit", "-framework", "QuartzCore", file, "-o", binary).CombinedOutput(); err != nil {
		t.Fatalf("compile native transition test: %v\n%s", err, output)
	}
	if output, err := exec.CommandContext(ctx, binary).CombinedOutput(); err != nil {
		t.Fatalf("native transition regression: %v\n%s", err, output)
	}
}
