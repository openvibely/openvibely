//go:build openvibely_update_testclock

package update

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// This control exists only in explicitly tagged E2E executables. Normal release
// builds cannot activate it through environment variables or HTTP requests.
func clockOffset() time.Duration {
	raw, err := os.ReadFile(os.Getenv("OPENVIBELY_UPDATE_TEST_CLOCK"))
	if err != nil {
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0
	}
	return time.Duration(value)
}

func updateClockNow() time.Time { return time.Now().Add(clockOffset()) }

func updateCheckTicks() (<-chan time.Time, func()) {
	if os.Getenv("OPENVIBELY_UPDATE_TEST_CLOCK") == "" {
		ticker := time.NewTicker(time.Minute)
		return ticker.C, ticker.Stop
	}
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(ticks)
		poll := time.NewTicker(10 * time.Millisecond)
		defer poll.Stop()
		last := time.Duration(-1)
		for {
			select {
			case <-done:
				return
			case <-poll.C:
				offset := clockOffset()
				if offset != last {
					select {
					case ticks <- time.Unix(0, int64(offset)):
						last = offset
					case <-done:
						return
					}
				}
			}
		}
	}()
	return ticks, func() { close(done) }
}

func acknowledgeUpdateTick(tick time.Time) {
	path := os.Getenv("OPENVIBELY_UPDATE_TEST_CLOCK")
	if path != "" {
		_ = os.WriteFile(path+".ack", []byte(strconv.FormatInt(tick.UnixNano(), 10)), 0600)
	}
}

func acknowledgeDesktopInstaller() {
	if path := os.Getenv("OPENVIBELY_UPDATE_TEST_CLOCK"); path != "" {
		_ = os.WriteFile(path+".ready", []byte("ready"), 0600)
	}
}
