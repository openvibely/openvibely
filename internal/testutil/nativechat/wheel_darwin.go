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
 class_addMethod(cls,@selector(window),imp_implementationWithBlock(^(id unused){return window;}),method_getTypeEncoding(class_getInstanceMethod([NSEvent class],@selector(window))));
 objc_registerClassPair(cls);object_setClass(event,cls);
 NSPoint local=[view convertPoint:point fromView:nil];
 NSView* target=[view hitTest:local];
 if(!target){CFRelease(cg);return 3;}
 [target scrollWheel:event];
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
