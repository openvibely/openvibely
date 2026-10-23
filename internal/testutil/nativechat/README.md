# Native chat regression host

This test-only Wails window loads the real task-thread HTTP fixture. The handler
suite seeds an isolated SQLite database with ten executions containing messages
and tool activity. It never opens the user's database.

One shared JavaScript scenario checks continuous upward input while history is
inserted, visible text positions, nested vertical/horizontal tool scrolling, and
leaving/reopening the cached task. It observes trusted native wheel events,
rather than substituting `scrollTop` assignments for the paging gesture.
The test runs sequential `during-wheel` and `after-wheel` subtests, using immediate
and delayed history responses. The delayed response waits for an explicit
input-finished signal from the browser, not a fixed timer. Each scenario asserts
when history arrives and monitors reading position through rendering completion.

Run on a graphical desktop (macOS, Windows, or Linux):

```
OPENVIBELY_RUN_NATIVE_UI=1 go test ./internal/handler -run '^TestTaskNativeWheelPagingInWails$' -count=1 -timeout 300s
```

PowerShell: set `$env:OPENVIBELY_RUN_NATIVE_UI='1'` before the `go test` command.
Linux needs GTK3/WebKitGTK 4.1 development packages and a display/window manager
(Xvfb and Openbox work in CI). The test builds its helper with `gtk3` on Linux.
GTK4 is not covered by this initial driver. macOS needs Xcode command-line tools.
Windows needs the WebView2 runtime.

Normal tests skip this native test unless opted in. The OS/architecture matrix
in `.github/workflows/test.yml` opts in. Failures/timeouts are test failures,
not silent skips. The helper process is stopped by the existing browser-test
process cleanup even on failure.

The only platform-specific pieces are native input delivery:

- macOS: AppKit wheel events sent through the Wails NSWindow's event dispatch,
  rather than directly invoking a private WebKit subview. Native hit testing
  converts the window point into the webview's **superview** coordinates, as
  required by [NSView.hitTest](https://developer.apple.com/documentation/appkit/nsview/hittest(_:)).
  A native regression checks this with an offset, flipped view hierarchy.
- Linux: GDK smooth scroll events routed to the Wails WebKitGTK view.
- Windows: wheel messages delivered to the WebView2 child window.

The driver endpoint listens only on loopback and requires the fixture's origin.
It exists only in the helper built with `openvibely_native_ui`; no production
window or server gains an input-injection endpoint.

This replaces the standalone Swift window. Native event delivery does not
reproduce every physical trackpad gesture or hardware-specific momentum profile.

The fixture installs the native-input bridge in its HTML before page scripts run;
this does not rely on Wails' platform-dependent `WebviewWindowOptions.JS` handling
for URL-backed windows. Gesture setup waits for virtual layout to settle and
calibrates pixel wheel units from a trusted event. Calibration awaits native
routing success as well as browser receipt; an HTTP acknowledgement alone is not
proof of delivery. Missing native targets return an error instead of silently
dropping input. macOS gestures include began, changed, and ended phases, with no
additional movement on end. The burst's 8ms spacing generates sustained input;
it is not a readiness delay. Assertions follow visible text
across late height corrections rather than assuming absolute scroll offsets are
constant. Event coalescing is allowed, but actual input, paging, and text tracking
must all be observed for the test to pass.
