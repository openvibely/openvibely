//go:build !openvibely_memory_diagnostics || !darwin

package main

// Memory profiling is absent from ordinary desktop builds.
func startDesktopMemoryDiagnostics() (func(), error) {
	return func() {}, nil
}
