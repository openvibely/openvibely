# Desktop memory diagnostics (macOS)

This diagnostic build exposes Go heap profiles for the **main desktop process**. It does not profile WebKit's separate WebContent/GPU processes. Activity Monitor's process footprint and Go heap size are different measurements.

## Build and launch

```sh
go build -tags openvibely_memory_diagnostics -o /tmp/openvibely-memory-diagnostic ./cmd/desktop
# Quit the existing desktop app before starting another backend on the same data.
OPENVIBELY_MEMORY_DIAGNOSTICS=1 /tmp/openvibely-memory-diagnostic
```

Keep symbols in this build; do not pass `-ldflags='-s -w'`.

The startup log prints a line such as:

```
[memory-diagnostics] pid=12345 socket=/tmp/ov-heap-123456/profile.sock
```

The socket lives in a unique directory with permissions 0700, and the socket has permissions 0600. There is no TCP listener, no authentication change to the application server, and no registration on the application's HTTP mux. Both the build tag and the environment variable are required. Ordinary builds contain only a no-op stub, including on non-macOS platforms.

## Capture before and after the same workload

Substitute the actual socket path from the startup log:

```sh
socket=/tmp/ov-heap-123456/profile.sock
curl --fail --silent --show-error --unix-socket "$socket" http://localhost/stats > baseline-stats.json
curl --fail --silent --show-error --unix-socket "$socket" 'http://localhost/heap?gc=1' > baseline-heap.pprof
curl --fail --silent --show-error --unix-socket "$socket" http://localhost/allocs > baseline-allocs.pprof
```

Open the same pages/threads repeatedly, return to the starting page, and capture again with `after-` filenames. `/stats` does not force garbage collection. `?gc=1` explicitly collects before the profile, so take process-footprint and stats measurements first. Heap profiles show sampled live Go allocations; allocation profiles are cumulative. A heap profile does not identify all native allocations, graphics surfaces, or memory the runtime reserves for reuse.

```sh
go tool pprof -top -inuse_space -base baseline-heap.pprof /tmp/openvibely-memory-diagnostic after-heap.pprof
go tool pprof -top -alloc_space -base baseline-allocs.pprof /tmp/openvibely-memory-diagnostic after-allocs.pprof
```

The only routes are GET `/stats`, `/heap`, and `/allocs`. No command-line, CPU, goroutine, or arbitrary runtime profiling routes are exposed. Profiles and stats are sensitive diagnostic artifacts; keep them local unless intentionally sharing them. The socket and directory are removed on normal shutdown. A forced kill may leave the directory behind; it remains private and a later launch uses a new unique directory.

## Tests

```sh
go test -race -tags openvibely_memory_diagnostics ./cmd/desktop -run '^TestDesktopMemoryDiagnostics' -count=1
```
