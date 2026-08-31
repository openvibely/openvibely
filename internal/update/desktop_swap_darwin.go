//go:build darwin

package update

import "golang.org/x/sys/unix"

func atomicExchangeInstallUnits(first, second string) error {
	return unix.RenamexNp(first, second, unix.RENAME_SWAP)
}
