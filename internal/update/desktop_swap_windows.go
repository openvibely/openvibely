//go:build windows

package update

import "os"

// Windows cannot atomically exchange non-empty directories. The desktop helper
// already keeps a rollback backup, so directory install units use a best-effort
// remove-and-publish swap after the old process exits.
func atomicExchangeInstallUnits(first, second string) error {
	firstInfo, firstErr := os.Stat(first)
	secondInfo, secondErr := os.Stat(second)
	if firstErr == nil && secondErr == nil && (firstInfo.IsDir() || secondInfo.IsDir()) {
		if err := os.RemoveAll(first); err != nil {
			return err
		}
		return os.Rename(second, first)
	}
	return atomicReplace(second, first)
}
