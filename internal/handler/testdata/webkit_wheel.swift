// Test-only AppKit input bridge. Events enter WebKit's native scrolling path;
// no global event injection or Accessibility permission is required.
import AppKit
import WebKit
import ObjectiveC

class WheelBridge: NSObject, WKScriptMessageHandler {
    weak var view: WKWebView?
    var timer: Timer?

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        guard let view = view, let window = view.window,
              let data = message.body as? [String: Double] else { return }
        if let count = data["count"], count > 0 {
            var remaining = Int(count)
            var payload = data
            payload.removeValue(forKey: "count")
            timer?.invalidate()
            // Continue input independently of the web process and its RAFs.
            timer = Timer.scheduledTimer(withTimeInterval: 0.008, repeats: true) { [weak self] timer in
                guard let self = self else { timer.invalidate(); return }
                self.sendWheel(view, window, payload)
                remaining -= 1
                if remaining <= 0 {
                    timer.invalidate()
                    view.evaluateJavaScript("window._nativeBurstDone = true", completionHandler: nil)
                }
            }
            return
        }
        sendWheel(view, window, data)
    }

    func sendWheel(_ view: WKWebView, _ window: NSWindow, _ data: [String: Double]) {
        let cg = CGEvent(scrollWheelEvent2Source: nil, units: .pixel, wheelCount: 2,
                         wheel1: Int32(-(data["dy"] ?? 0)), wheel2: Int32(-(data["dx"] ?? 0)), wheel3: 0)!
        cg.setIntegerValueField(.scrollWheelEventIsContinuous, value: 1)
        cg.setIntegerValueField(.scrollWheelEventScrollPhase, value: 2)
        let event = NSEvent(cgEvent: cg)!
        // Locally created CGEvents have no AppKit target window. Override only
        // routing coordinates, retaining the real NSEvent wheel payload used by
        // WebKit. Each test runs in its own short-lived native process.
        let cls: AnyClass = objc_allocateClassPair(object_getClass(event), "LocalWheel" + UUID().uuidString, 0)!
        let point = NSPoint(x: data["x"] ?? 100, y: view.bounds.height - (data["y"] ?? 100))
        let location: @convention(block) (AnyObject) -> NSPoint = { _ in point }
        let target: @convention(block) (AnyObject) -> NSWindow = { _ in window }
        for (name, implementation) in [("locationInWindow", imp_implementationWithBlock(location)),
                                        ("window", imp_implementationWithBlock(target))] {
            let selector = NSSelectorFromString(name)
            class_addMethod(cls, selector, implementation,
                            method_getTypeEncoding(class_getInstanceMethod(NSEvent.self, selector)!))
        }
        objc_registerClassPair(cls)
        object_setClass(event, cls)
        (view.hitTest(point) ?? view).scrollWheel(with: event)
    }
}

let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1728, height: 1084),
                      styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
let configuration = WKWebViewConfiguration()
let bridge = WheelBridge()
configuration.userContentController.add(bridge, name: "nativeWheel")
let view = WKWebView(frame: window.contentView!.bounds, configuration: configuration)
bridge.view = view
window.contentView!.addSubview(view)
window.orderFrontRegardless()
view.load(URLRequest(url: URL(string: CommandLine.arguments.last!)!))
app.run()
