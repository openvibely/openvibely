# Go backend profiling

Profiling is disabled by default. Set `OPENVIBELY_PPROF_PORT=6060` in the environment when launching a rebuilt server or desktop app. Both use the shared backend startup. For example, from the repository:

```sh
OPENVIBELY_PPROF_PORT=6060 go run ./cmd/server
```

The standard-library profiler listens only on `127.0.0.1`, separately from application routes. No application login is required on this local diagnostic listener. Leave the variable unset for normal operation; restart without it to disable profiling. Port `0` chooses an available port, printed in the startup log. Invalid ports or an occupied port fail startup explicitly. Profiles can contain sensitive application details; keep captures local.

Warm up the application before capturing a baseline. Use the same running process and build for both snapshots:

```sh
curl --fail -o before.pb.gz 'http://127.0.0.1:6060/debug/pprof/heap?gc=1'
# Repeat the activity under investigation, then let requests/workers finish.
curl --fail -o after.pb.gz 'http://127.0.0.1:6060/debug/pprof/heap?gc=1'
go tool pprof -http=127.0.0.1:8081 -sample_index=inuse_space -base=before.pb.gz after.pb.gz
```

`gc=1` requests collection before capture and can briefly affect latency. `inuse_space` shows sampled live bytes; `inuse_objects` shows live object counts. Repeat equal workload batches to distinguish bounded caches from continuing retention. Profiles identify allocation stacks, not the references retaining objects.

To inspect allocation churn during 30 seconds of activity:

```sh
curl --fail -o allocations.pb.gz 'http://127.0.0.1:6060/debug/pprof/allocs?seconds=30'
go tool pprof -alloc_space allocations.pb.gz
curl --fail -o goroutines.txt 'http://127.0.0.1:6060/debug/pprof/goroutine?debug=2'
```

CPU profiling is available at `/debug/pprof/profile?seconds=30`, and execution tracing at `/debug/pprof/trace?seconds=5`. Block/mutex sampling is not enabled by this option. Go heap profiles do not include WebKit graphics buffers or all native allocations. No third-party profiling dependency is needed.
