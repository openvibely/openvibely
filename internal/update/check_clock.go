//go:build !openvibely_update_testclock

package update

import "time"

func updateClockNow() time.Time { return time.Now() }

func updateCheckTicks() (<-chan time.Time, func()) {
	ticker := time.NewTicker(time.Minute)
	return ticker.C, ticker.Stop
}

func acknowledgeUpdateTick(time.Time) {}

func acknowledgeDesktopInstaller() {}
