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
static void sendWheel(void* pointer, double x,double y,double dx,double dy) {
 NSWindow* window=(NSWindow*)pointer;
 NSView* view=findWebView(window.contentView);if(!view)return;
 CGEventRef cg=CGEventCreateScrollWheelEvent(NULL,kCGScrollEventUnitPixel,2,(int)-dy,(int)-dx);
 CGEventSetIntegerValueField(cg,kCGScrollWheelEventIsContinuous,1);
 CGEventSetIntegerValueField(cg,kCGScrollWheelEventScrollPhase,2);
 NSEvent* event=[NSEvent eventWithCGEvent:cg];
 // Preserve NSEvent's internal wheel payload; only replace local routing.
 NSPoint point=[view convertPoint:NSMakePoint(x,view.isFlipped?y:view.bounds.size.height-y) toView:nil];
 Class cls=objc_allocateClassPair(object_getClass(event),[[NSUUID UUID].UUIDString UTF8String],0);
 class_addMethod(cls,@selector(locationInWindow),imp_implementationWithBlock(^(id unused){return point;}),method_getTypeEncoding(class_getInstanceMethod([NSEvent class],@selector(locationInWindow))));
 class_addMethod(cls,@selector(window),imp_implementationWithBlock(^(id unused){return window;}),method_getTypeEncoding(class_getInstanceMethod([NSEvent class],@selector(window))));
 objc_registerClassPair(cls);object_setClass(event,cls);
 NSPoint local=[view convertPoint:point fromView:nil];
 [[view hitTest:local] scrollWheel:event];
 CFRelease(cg);
}
*/
import "C"
import "unsafe"

func nativeWheel(window unsafe.Pointer, e wheel) {
	C.sendWheel(window, C.double(e.X), C.double(e.Y), C.double(e.DX), C.double(e.DY))
}
