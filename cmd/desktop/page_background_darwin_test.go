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

func TestNativePageBackgroundPreservesRoundedClip(t *testing.T) {
	source, err := os.ReadFile("page_background_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start, end := strings.Index(text, "#import <Cocoa/Cocoa.h>"), strings.Index(text, "*/")
	if start < 0 || end < start {
		t.Fatal("missing native implementation")
	}
	harness := text[start:end] + `
@interface OVBackgroundTestWindow : NSWindow <OVPageBackgroundHost>
@property (retain) WKWebView *webView;
@end
@implementation OVBackgroundTestWindow
@end
int main(void) {
 @autoreleasepool {
  [NSApplication sharedApplication];
  OVBackgroundTestWindow *window = [[OVBackgroundTestWindow alloc] initWithContentRect:NSMakeRect(0,0,400,250) styleMask:NSWindowStyleMaskBorderless backing:NSBackingStoreBuffered defer:NO];
  window.contentView.wantsLayer = YES;
  window.contentView.layer.cornerRadius = 20;
  window.contentView.layer.masksToBounds = YES;
  window.webView = [[WKWebView alloc] initWithFrame:window.contentView.bounds];
  [window.contentView addSubview:window.webView];
  for (int light = 0; light < 2; light++) {
   int c = light ? 245 : 21;
   ovSetPageBackground(window, c, c, c);
   if (window.opaque || window.backgroundColor.alphaComponent != 0) return 1;
   if (window.contentView.layer.cornerRadius != 20 || !window.contentView.layer.masksToBounds) return 2;
   NSColor *paint = [[NSColor colorWithCGColor:window.contentView.layer.backgroundColor] colorUsingColorSpace:NSColorSpace.sRGBColorSpace];
   if (fabs(paint.redComponent - c/255.0) > .001) return 3;
   if (@available(macOS 12.0, *)) {
    NSColor *under = [window.webView.underPageBackgroundColor colorUsingColorSpace:NSColorSpace.sRGBColorSpace];
    if (fabs(under.redComponent - c/255.0) > .001) return 4;
   }
   if ([[window.webView valueForKey:@"drawsBackground"] boolValue]) return 5;
  }
 }
 return 0;
}
`
	dir := t.TempDir()
	file, binary := filepath.Join(dir, "background.m"), filepath.Join(dir, "background")
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "clang", "-framework", "Cocoa", "-framework", "WebKit", "-framework", "QuartzCore", file, "-o", binary).CombinedOutput(); err != nil {
		t.Fatalf("compile native background test: %v\n%s", err, output)
	}
	if output, err := exec.CommandContext(ctx, binary).CombinedOutput(); err != nil {
		t.Fatalf("native background regression: %v\n%s", err, output)
	}
}
