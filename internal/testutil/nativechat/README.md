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

The same host also checks automatic scrolling and desktop shell isolation:

```
OPENVIBELY_RUN_NATIVE_UI=1 go test ./internal/handler -run '^TestNativePanelScrollIsolationInWails$' -count=1 -v -timeout 300s
```

This scenario renders the production desktop layout, Analytics, approval thread,
and task sidebar against isolated HTTP data. It clicks the real usage toast,
opens an approval link, switches away and restores the saved project tab, and
uses sidebar keyboard navigation to reveal a row through nested panels. It
samples titlebar/tab positions and shell scroll offsets throughout navigation,
and checks that each target becomes visible. Clicks and key events are dispatched
by the fixture JavaScript; layout and scrolling run in the native Wails engine
(WKWebView, WebKitGTK, or WebView2), not Chrome. It does not validate physical
pointer delivery or the packaged application's custom native titlebar controls.
The short-sidebar case constrains the sidebar height without resizing the native
window. CI runs this test in the existing OS/architecture matrix.

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
  The underlying CoreGraphics event also receives the intended screen position
  before conversion to NSEvent; its location must agree with local window
  routing rather than retaining the physical cursor position. A regression
  checks the AppKit-to-Quartz conversion, including secondary-screen positions.
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

On macOS the host logs the first native event of each gesture (decoded deltas,
phase, hit-test target, coordinates, and window activation/visibility). These
lines are included in the native process output when a scenario fails. This
separates a missing DOM event from malformed native input or inactive routing;
a successful HTTP response alone still does not prove browser delivery.

## Desktop task-list alignment

The same host also runs the real desktop shell in each OS's webview:

```sh
OPENVIBELY_RUN_NATIVE_UI=1 go test ./internal/handler -run '^TestNativeDesktopTaskListAlignment$' -count=1 -timeout 420s
```

CI runs this on the existing Linux, Windows, and macOS architecture matrix.
The fixture uses frameless 500px and 1280px windows and checks floating/unified
layouts, attached/detached tabs, and expanded/collapsed task lists. It compares
actual toggle and rail geometry, checks the toggle's hit target and header action
positions, verifies keyboard tab navigation does not scroll the surrounding shell,
and preserves macOS's 80px traffic-light reservation. Linux also checks
right-side and left-side caption insets, including grouped left controls.

This tests real WebKitGTK/WebView2/WKWebView rendering. Caption geometry is supplied
by the fixture through the production CSS attributes/properties; the host does
not install the production native caption overlay. Native caption tests cover
that integration separately. `OPENVIBELY_NATIVE_LAYOUT_WIDTH` selects the
frameless viewport for this test and leaves the scrolling host's defaults intact.

For a quicker check in Chrome on the current OS:

```sh
go test ./internal/handler -run '^TestDesktopTaskListAlignmentInChrome$' -count=1 -timeout 240s
```

## Visible host and streaming memory measurements (macOS)

The larger always-on-top host previously kept as `/tmp/ov-memory-audit/native-visible`
can now be built directly from this repository. No replacement `main.go`, overlay,
or files from a previous `/tmp` session are required:

```sh
go build -tags=openvibely_native_ui -o /tmp/ov-native-visible ./internal/testutil/nativechat
OPENVIBELY_NATIVE_VISIBLE=1 /tmp/ov-native-visible http://127.0.0.1:PORT/FIXTURE
```

The URL must be a running isolated test fixture accepting `/native-test-bridge`.
This is not a standalone OV server. `OPENVIBELY_NATIVE_VISIBLE=1` selects a
1728×1085 window, always-on-top, with macOS all-Spaces/full-screen-auxiliary
behavior. Without it, the existing native wheel tests retain their normal window.

To build the host and run the complete streaming memory diagnostic automatically:

```sh
OPENVIBELY_RUN_STREAM_MEMORY=1 \
OPENVIBELY_STREAM_RESULTS="$PWD/artifacts/stream-memory" \
go test ./internal/handler -run '^TestStreamMemoryDiagnostic$' -count=1 -v -timeout 6m
```

Requires a graphical macOS session, Xcode command-line tools, `vmmap`, `footprint`,
and `caffeinate`. The diagnostic prevents idle display sleep while the host runs.
Keep the test window visible; do not minimize it or launch unrelated WebKit apps
while it identifies the new WebContent process. Ambiguous process identification,
failed memory sampling, hidden windows, JavaScript errors, or a final-content
mismatch fail the diagnostic. It uses handler test helpers and an isolated SQLite
database, never the user's database or live model credentials.

Each run creates a fresh `ov-stream-memory-*` directory inside the result directory
(or the OS temporary directory if unset), and prints its path. It records:

- A 16,800-character seed followed by 1,000 21-character chunks requested every
  20 ms through the real resumed-stream renderer. Heavy rendering can delay timers.
- JSON measurements at baseline, approximately each 250 chunks, after settling,
  and after 30 seconds idle: render calls, elapsed time, DOM size, viewport, and
  correctness checks.
- WebContent `vmmap -summary` and `footprint` output at each stage, plus `host.log`.

For before/after comparisons, run this same diagnostic on each implementation,
using separate result directories and the same window size and streaming interval.
Repeat runs and alternate order. To isolate a renderer change, do not also change
its throttle. The diagnostic measures the currently checked-out implementation;
it does not assume the experimental DOM-morph helper exists.

This is a measurement tool, not a fixed performance-budget assertion. Sampling can
perturb timing. `owned unmapped (graphics)` resident and dirty/charged bytes are
different from process physical footprint. Graphics maxima are sampled, not
continuous peaks. Neither these values nor render counts measure allocation churn
or garbage collection. This single synthetic resumed stream does not establish
behavior for concurrent streams, fresh-stream setup, every rich-content type, or
the full running app. Backend `pprof` profiling is separate.

## Native browser storage

Tests build against unmodified Wails. Before starting each native host, the
shared `testutil.ConfigureNativeBrowserStorage` helper creates fresh storage
inside `t.TempDir()` and sets only the child process environment:

- macOS: `CFFIXED_USER_HOME` redirects Cocoa home-directory resolution, including
  WebKit data and caches. This is a Core Foundation implementation setting;
  the native storage regression guards its behavior on supported macOS runners.
- Linux: `XDG_DATA_HOME` and `XDG_CACHE_HOME` redirect WebKitGTK storage.
- Windows: `APPDATA` redirects Wails’ default WebView2 profile directory. Wails’
  custom loader clears `WEBVIEW2_USER_DATA_FOLDER`, so tests do not use it.

Hosts must stop before test cleanup removes their storage. The memory diagnostic
uses the same isolation while retaining its explicitly collected result files.
Manual host launches do not get automatic isolation; supply the platform's
variable(s) when launching manually if temporary storage is desired.

Run the macOS isolation regression with the normal Go command:

```sh
OPENVIBELY_RUN_NATIVE_UI=1 go test -tags=openvibely_native_ui ./web/templates/pages -run '^TestNativeWebKitStorageIsolation$' -count=1 -timeout=120s
```

Native test hosts watch a shutdown request file under the test directory and call
`app.Quit()`. Parents wait for exit, using forced termination only on timeout.
Cleanup retries transient deletion errors; it does not infer browser shutdown
from a fixed period without filesystem writes. Wails does not explicitly promise
that every OS-managed browser service has exited when the host exits.
