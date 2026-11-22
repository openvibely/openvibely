//go:build openvibely_update_testclock

package update

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestE2EClockAdvancesClientAndAcknowledgesChecks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clock")
	t.Setenv("OPENVIBELY_UPDATE_TEST_CLOCK", path)
	if err := os.WriteFile(path, []byte("0"), 0600); err != nil {
		t.Fatal(err)
	}
	ticks, stop := updateCheckTicks()
	defer stop()
	select {
	case <-ticks:
	case <-time.After(time.Second):
		t.Fatal("initial tick missing")
	}
	advance := 25 * time.Hour
	value := strconv.FormatInt(int64(advance), 10)
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case tick := <-ticks:
		if tick.UnixNano() != int64(advance) {
			t.Fatalf("wrong advancement: %v", tick)
		}
		acknowledgeUpdateTick(tick)
	case <-time.After(time.Second):
		t.Fatal("advanced tick missing")
	}
	client := NewClient(ClientConfig{})
	delta := client.cfg.Now().Sub(time.Now())
	if delta < advance-time.Second || delta > advance+time.Second {
		t.Fatalf("client did not advance: %v", delta)
	}
	raw, err := os.ReadFile(path + ".ack")
	if err != nil || string(raw) != value {
		t.Fatalf("acknowledgement=%q err=%v", raw, err)
	}
}
