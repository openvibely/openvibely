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
// AppKit screen points have a bottom-left origin; Quartz uses the top-left
// of the primary screen. Both are in points, including on Retina displays.
static CGEventRef createWheelEvent(NSPoint screenPoint, CGFloat primaryTop, double dx, double dy, int phase) {
 CGEventRef cg=CGEventCreateScrollWheelEvent(NULL,kCGScrollEventUnitPixel,2,(int)-dy,(int)-dx);
 CGEventSetLocation(cg,CGPointMake(screenPoint.x,primaryTop-screenPoint.y));
 CGEventSetIntegerValueField(cg,kCGScrollWheelEventIsContinuous,1);
 CGEventSetIntegerValueField(cg,kCGScrollWheelEventScrollPhase,phase);
 return cg;
}
static int checkWheelScreenCoordinates(void) {
 @autoreleasepool {
  // Include secondary-screen coordinates outside the primary display.
  NSPoint inputs[]={NSMakePoint(590,787),NSMakePoint(-400,1200),NSMakePoint(2100,-50)};
  CGPoint expected[]={CGPointMake(590,113),CGPointMake(-400,-300),CGPointMake(2100,950)};
  for(int i=0;i<3;i++) {
   CGEventRef cg=createWheelEvent(inputs[i],900,0,40,1);
   NSEvent* event=[NSEvent eventWithCGEvent:cg];
   BOOL correct=CGPointEqualToPoint(CGEventGetLocation(cg),expected[i]) &&
    CGPointEqualToPoint(CGEventGetLocation(event.CGEvent),expected[i]) &&
    event.scrollingDeltaY==-40 && event.phase==NSEventPhaseBegan;
   CFRelease(cg);
   if(!correct)return 0;
  }
  return 1;
 }
}
static int sendWheel(void* pointer, double x,double y,double dx,double dy,int phase) {
 NSWindow* window=(NSWindow*)pointer;
 if(!window || !window.isVisible)return 1;
 NSView* view=findWebView(window.contentView);if(!view)return 2;
 NSPoint point=[view convertPoint:NSMakePoint(x,view.isFlipped?y:view.bounds.size.height-y) toView:nil];
 NSPoint screenPoint=[window convertPointToScreen:point];
 CGEventRef cg=createWheelEvent(screenPoint,NSMaxY(NSScreen.screens.firstObject.frame),dx,dy,phase);
 NSEvent* event=[NSEvent eventWithCGEvent:cg];
 // Preserve the wheel payload and make local routing agree with its Quartz
 // location. Overriding only locationInWindow leaves the original cursor
 // position in CGEvent, which native consumers can also consult.
 Class cls=objc_allocateClassPair(object_getClass(event),[[NSUUID UUID].UUIDString UTF8String],0);
 class_addMethod(cls,@selector(locationInWindow),imp_implementationWithBlock(^(id unused){return point;}),method_getTypeEncoding(class_getInstanceMethod([NSEvent class],@selector(locationInWindow))));
 class_addMethod(cls,@selector(windowNumber),imp_implementationWithBlock(^NSInteger(id unused){return window.windowNumber;}),method_getTypeEncoding(class_getInstanceMethod([NSEvent class],@selector(windowNumber))));
 class_addMethod(cls,@selector(window),imp_implementationWithBlock(^(id unused){return window;}),method_getTypeEncoding(class_getInstanceMethod([NSEvent class],@selector(window))));
 objc_registerClassPair(cls);object_setClass(event,cls);
 NSView* target=wheelTarget(view,point);
 if(!target){CFRelease(cg);return 3;}
 // Browser-side capture cannot distinguish an invalid native payload from
 // AppKit dropping/rerouting it. Log each gesture's first event in the test
 // process output, which the harness includes on failure. Do not log every
 // frame of the burst or modify focus/input state to hide the failure.
 if(phase==1) {
  NSPoint local=[view convertPoint:event.locationInWindow fromView:nil];
  CGPoint cgPoint=CGEventGetLocation(cg);
  NSLog(@"[native-wheel] requested=(%.2f,%.2f) delta=(%.2f,%.2f) type=%lu precise=%d phase=%lu momentum=%lu window=%ld key=%d main=%d appActive=%d occlusion=%lu viewHidden=%d viewFrame=%@ windowFrame=%@ local=%@ cg=(%.2f,%.2f) target=%@ responder=%@ macOS=%@",
   dx,dy,event.scrollingDeltaX,event.scrollingDeltaY,(unsigned long)event.type,
   event.hasPreciseScrollingDeltas,(unsigned long)event.phase,(unsigned long)event.momentumPhase,
   (long)event.windowNumber,window.isKeyWindow,window.isMainWindow,NSApp.isActive,
   (unsigned long)window.occlusionState,view.isHiddenOrHasHiddenAncestor,
   NSStringFromRect(view.frame),NSStringFromRect(window.frame),NSStringFromPoint(local),
   cgPoint.x,cgPoint.y,NSStringFromClass(target.class),NSStringFromClass(window.firstResponder.class),
   NSProcessInfo.processInfo.operatingSystemVersionString);
 }
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

func checkNativeWheelScreenCoordinates() bool { return C.checkWheelScreenCoordinates() != 0 }
