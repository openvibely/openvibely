//go:build openvibely_native_ui

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include <stdlib.h>

// AppKit's native destination protocol with a real file pasteboard. This
// exercises WKWebView's native import/IPC/file grants without global pointer
// injection or Accessibility permissions on hosted macOS runners.
@interface OVTestFileDrag : NSObject <NSDraggingInfo>
@property(retain) NSWindow *testWindow;
@property(retain) NSPasteboard *testPasteboard;
@property(retain) NSURL *testURL;
@property NSPoint testPoint;
@property NSDraggingFormation draggingFormation;
@property NSInteger numberOfValidItemsForDrop;
@property BOOL animatesToDestination;
@property NSInteger draggingLeaderIndex;
@end
@implementation OVTestFileDrag
- (NSWindow *)draggingDestinationWindow { return self.testWindow; }
- (NSDragOperation)draggingSourceOperationMask { return NSDragOperationCopy; }
- (NSPoint)draggingLocation { return self.testPoint; }
- (NSPoint)draggedImageLocation { return self.testPoint; }
- (NSImage *)draggedImage { return nil; }
- (NSPasteboard *)draggingPasteboard { return self.testPasteboard; }
- (id)draggingSource { return nil; } // External file source, not an HTML drag.
- (NSInteger)draggingSequenceNumber { return 1; }
- (void)slideDraggedImageTo:(NSPoint)point {}
- (NSArray *)namesOfPromisedFilesDroppedAtDestination:(NSURL *)destination { return nil; }
- (void)enumerateDraggingItemsWithOptions:(NSDraggingItemEnumerationOptions)options forView:(NSView *)view classes:(NSArray *)classes searchOptions:(NSDictionary *)searchOptions usingBlock:(void (^)(NSDraggingItem *, NSInteger, BOOL *))block {
 NSDraggingItem *item = [[NSDraggingItem alloc] initWithPasteboardWriter:self.testURL];
 BOOL stop = NO;
 block(item, 0, &stop);
 [item release];
}
- (void)dealloc {
 [self.testPasteboard releaseGlobally];
 [_testPasteboard release]; [_testWindow release]; [_testURL release];
 [super dealloc];
}
@end
static WKWebView *dropWebView(NSView *view) {
 if ([view isKindOfClass:WKWebView.class]) return (WKWebView *)view;
 for (NSView *child in view.subviews) { WKWebView *found = dropWebView(child); if (found) return found; }
 return nil;
}
static OVTestFileDrag *fileDrag;
static WKWebView *fileDropView;
static int nativeDropStage(void *pointer, const char *file, double x, double y, int stage) {
 @autoreleasepool {
  if (stage == 0) {
   [fileDrag release]; fileDrag = nil;
   [fileDropView release]; fileDropView = nil;
   return 0;
  }
  NSWindow *window = (NSWindow *)pointer;
  if (!window || !window.isVisible) return 1;
  if (stage == 1) {
   nativeDropStage(pointer, file, x, y, 0);
   WKWebView *view = dropWebView(window.contentView);
   if (!view) return 2;
   fileDropView = [view retain];
   fileDrag = [[OVTestFileDrag alloc] init];
   fileDrag.testWindow = window;
   fileDrag.testURL = [NSURL fileURLWithPath:[NSString stringWithUTF8String:file]];
   fileDrag.testPasteboard = [NSPasteboard pasteboardWithUniqueName];
   [fileDrag.testPasteboard writeObjects:@[fileDrag.testURL]];
   // Finder includes the legacy file list as well. WKWebView uses it to grant
   // the web/network processes access to files before delivering the drop.
   [fileDrag.testPasteboard setPropertyList:@[fileDrag.testURL.path] forType:NSFilenamesPboardType];
   fileDrag.testPoint = [view convertPoint:NSMakePoint(x, view.isFlipped ? y : view.bounds.size.height-y) toView:nil];
   fileDrag.numberOfValidItemsForDrop = 1;
   [view draggingEntered:fileDrag];
  } else if (stage == 2) {
   if (!fileDrag) return 3;
   [fileDropView draggingUpdated:fileDrag];
  } else if (stage == 3) {
   if (!fileDrag) return 3;
   if (![fileDropView prepareForDragOperation:fileDrag]) return 4;
   if (![fileDropView performDragOperation:fileDrag]) return 5;
   // Keep the pasteboard alive until host shutdown (or the next drop): WebKit
   // reads it asynchronously after this call. No JavaScript File is fabricated.
  }
  return 0;
 }
}
*/
import "C"

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func nativeFileDrop(ctx context.Context, window *application.WebviewWindow, e fileDrop, path string) error {
	file := C.CString(path)
	defer C.free(unsafe.Pointer(file))
	completed := false
	defer func() {
		if !completed {
			application.InvokeSync(func() { C.nativeDropStage(window.NativeWindow(), file, 0, 0, 0) })
		}
	}()
	// Allow WebKit's asynchronous drag-enter/over replies to reach AppKit between
	// native stages. The test still requires trusted DOM events and a real upload.
	for _, stage := range []int{1, 2, 2, 3} {
		if err := ctx.Err(); err != nil {
			return err
		}
		var code C.int
		application.InvokeSync(func() {
			code = C.nativeDropStage(window.NativeWindow(), file, C.double(e.X), C.double(e.Y), C.int(stage))
		})
		if code != 0 {
			return fmt.Errorf("AppKit file drag stage %d failed: %d", stage, int(code))
		}
		if stage != 3 {
			timer := time.NewTimer(200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	completed = true
	return nil
}
