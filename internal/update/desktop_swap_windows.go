//go:build windows

package update

// Windows desktop install units are executable files. MoveFileEx replaces the
// destination atomically after the old process has exited.
func atomicExchangeInstallUnits(first, second string) error {
	return atomicReplace(second, first)
}
