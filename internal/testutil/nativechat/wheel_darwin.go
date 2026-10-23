//go:build openvibely_native_ui

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>
static NSView* findWebView(NSView* view) {
 if ([view isKindOfClass:[WKWebView class]]) return view;
 for(NSView* child in view.subviews) { NSView* result=findWebView(child);if(result)return result; }
 return nil;
}
// NSView hitTest: consumes superview coordinates, not receiver coordinates.
static NSView* wheelTarget(NSView* view, NSPoint windowPoint) {
 NSPoint parentPoint=[view.superview convertPoint:windowPoint fromView:nil];
 return [view hitTest:parentPoint];
}

// Exercise routing independently of WebKit timing. Offset and flipped views
// expose the coordinate-space mistake even when a full-size webview hides it.
@interface WheelTestFlippedView : NSView
@end
@implementation WheelTestFlippedView
- (BOOL)isFlipped { return YES; }
@end
static int checkWheelTargetCoordinates(void) {
 @autoreleasepool {
  NSView* root=[[NSView alloc] initWithFrame:NSMakeRect(0,0,800,600)];
  NSView* view=[[WheelTestFlippedView alloc] initWithFrame:NSMakeRect(150,90,400,300)];
  NSView* target=[[NSView alloc] initWithFrame:NSMakeRect(20,30,50,50)];
  [root addSubview:view];[view addSubview:target];
  NSPoint point=[view convertPoint:NSMakePoint(35,45) toView:nil];
  BOOL correct=wheelTarget(view,point)==target;
  BOOL oldWrong=[view hitTest:[view convertPoint:point fromView:nil]]!=target;
  [target release];[view release];[root release];
  return correct && oldWrong;
 }
}
static int sendWheel(void* pointer, double x,double y,double dx,double dy,int phase) {
 NSWindow* window=(NSWindow*)pointer;
 if(!window || !window.isVisible)return 1;
 NSView* view=findWebView(window.contentView);if(!view)return 2;
 CGEventRef cg=CGEventCreateScrollWheelEvent(NULL,kCGScrollEventUnitPixel,2,(int)-dy,(int)-dx);
 CGEventSetIntegerValueField(cg,kCGScrollWheelEventIsContinuous,1);
 CGEventSetIntegerValueField(cg,kCGScrollWheelEventScrollPhase,phase);
 NSEvent* event=[NSEvent eventWithCGEvent:cg];
 // Preserve NSEvent's internal wheel payload; only replace local routing.
 NSPoint point=[view convertPoint:NSMakePoint(x,view.isFlipped?y:view.bounds.size.height-y) toView:nil];
 Class cls=objc_allocateClassPair(object_getClass(event),[[NSUUID UUID].UUIDString UTF8String],0);
 class_addMethod(cls,@selector(locationInWindow),imp_implementationWithBlock(^(id unused){return point;}),method_getTypeEncoding(class_getInstanceMethod([NSEvent class],@selector(locationInWindow))));
 class_addMethod(cls,@selector(windowNumber),imp_implementationWithBlock(^NSInteger(id unused){return window.windowNumber;}),method_getTypeEncoding(class_getInstanceMethod([NSEvent class],@selector(windowNumber))));
 class_addMethod(cls,@selector(window),imp_implementationWithBlock(^(id unused){return window;}),method_getTypeEncoding(class_getInstanceMethod([NSEvent class],@selector(window))));
 objc_registerClassPair(cls);object_setClass(event,cls);
 NSView* target=wheelTarget(view,point);
 if(!target){CFRelease(cg);return 3;}
 // Enter AppKit through the window so its gesture routing and event context
 // are established. Directly calling a hit-tested private WebKit subview
 // bypasses that path and can acknowledge input without a DOM wheel event.
 [window sendEvent:event];
 CFRelease(cg);
 return 0;
}
*/
import "C"
import (
	"fmt"
	"unsafe"
)

func nativeWheel(window unsafe.Pointer, e wheel) error {
	code := C.sendWheel(window, C.double(e.X), C.double(e.Y), C.double(e.DX), C.double(e.DY), C.int(e.phase))
	if code != 0 {
		return fmt.Errorf("native wheel routing failed: %s", map[int]string{1: "window not visible", 2: "webview missing", 3: "no hit-test target"}[int(code)])
	}
	return nil
}

func checkNativeWheelCoordinates() bool { return C.checkWheelTargetCoordinates() != 0 }
