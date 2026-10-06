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

func TestNativeJavaScriptConfirmation(t *testing.T) {
	if os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("set OPENVIBELY_RUN_NATIVE_UI=1 to exercise real WebKit confirmation sheets")
	}
	source, err := os.ReadFile("confirm_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start, end := strings.Index(text, "#import <Cocoa/Cocoa.h>"), strings.Index(text, "*/")
	if start < 0 || end < start {
		t.Fatal("native confirmation implementation missing")
	}
	harness := text[start:end] + `
@interface WebviewWindowDelegate : NSObject <WKUIDelegate>
@end
@implementation WebviewWindowDelegate
@end
static BOOL pumpUntil(BOOL (^condition)(void)) {
 NSDate *deadline = [NSDate dateWithTimeIntervalSinceNow:10];
 while (!condition() && [deadline timeIntervalSinceNow] > 0) {
  [[NSRunLoop currentRunLoop] runUntilDate:[NSDate dateWithTimeIntervalSinceNow:0.01]];
 }
 return condition();
}
int main(void) {
 @autoreleasepool {
  [NSApplication sharedApplication];
  [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
  [NSApp finishLaunching];
  ovInstallConfirmationDialogs();
  ovInstallConfirmationDialogs(); // Repeated installation must preserve the callback.
  NSWindow *window = [[NSWindow alloc] initWithContentRect:NSMakeRect(0,0,500,300) styleMask:NSWindowStyleMaskTitled backing:NSBackingStoreBuffered defer:NO];
  WKWebView *webView = [[WKWebView alloc] initWithFrame:window.contentView.bounds];
  WebviewWindowDelegate *delegate = [[WebviewWindowDelegate alloc] init];
  webView.UIDelegate = delegate;
  [window.contentView addSubview:webView];
  [window makeKeyAndOrderFront:nil];
  [webView loadHTMLString:@"<html><body>Confirmation regression test</body></html>" baseURL:nil];
  if (!pumpUntil(^BOOL{ return !webView.loading; })) return 1;
  for (int accept = 0; accept < 2; accept++) {
   __block BOOL done = NO;
   __block BOOL result = NO;
   __block BOOL failed = NO;
   [webView evaluateJavaScript:@"window.confirm('Clear all chat history? This cannot be undone.')" completionHandler:^(id value, NSError *error) {
    result = [value boolValue]; failed = error != nil; done = YES;
   }];
   if (!pumpUntil(^BOOL{ return window.attachedSheet != nil || done; })) return 2;
   // Without the delegate WebKit returns false without displaying a sheet.
   if (!window.attachedSheet) return 3;
   [NSApp endSheet:window.attachedSheet returnCode:accept ? NSAlertFirstButtonReturn : NSAlertSecondButtonReturn];
   if (!pumpUntil(^BOOL{ return done; })) return 4;
   if (failed || result != (BOOL)accept) return 5;
   if (!pumpUntil(^BOOL{ return window.attachedSheet == nil; })) return 6;
  }
  [window orderOut:nil];
  webView.UIDelegate = nil;
  [delegate release];
  [webView release];
  [window release];
 }
 return 0;
}
`
	dir := t.TempDir()
	file, binary := filepath.Join(dir, "confirm.m"), filepath.Join(dir, "confirm")
	if err := os.WriteFile(file, []byte(harness), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "clang", "-fblocks", "-framework", "Cocoa", "-framework", "WebKit", file, "-o", binary).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, output)
	}
	if output, err := exec.CommandContext(ctx, binary).CombinedOutput(); err != nil {
		t.Fatalf("native confirmation: %v\n%s", err, output)
	}
}
