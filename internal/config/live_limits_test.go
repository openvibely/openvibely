package config

import (
	"testing"

	"github.com/openvibely/openvibely/internal/livelimits"
	"github.com/stretchr/testify/require"
)

func TestLiveLimitsConfiguration(t *testing.T) {
	names := []string{
		"OPENVIBELY_LIVE_MAX_CONNECTIONS",
		"OPENVIBELY_LIVE_MAX_SUBSCRIPTIONS_PER_CONNECTION",
		"OPENVIBELY_LIVE_MAX_PROJECT_SUBSCRIPTIONS",
		"OPENVIBELY_LIVE_MAX_EXECUTION_SUBSCRIPTIONS",
	}
	defaults := livelimits.Limits{Connections: 500, SubscriptionsPerConnection: 200, LiveSubscriptions: 5000, ExecutionSubscriptions: 25000}
	for _, mode := range []RuntimeMode{ModeServer, ModeDesktop} {
		t.Run(string(mode), func(t *testing.T) {
			t.Setenv("OPENVIBELY_APP_DATA_DIR", t.TempDir())
			for _, name := range names {
				t.Setenv(name, "")
			}
			require.Equal(t, defaults, LoadWithMode(mode).LiveLimits)
			for _, invalid := range []string{"0", "-1", "unlimited", "999999999999999999999999999999"} {
				for _, name := range names {
					t.Setenv(name, invalid)
				}
				require.Equal(t, defaults, LoadWithMode(mode).LiveLimits, invalid)
			}
			for i, value := range []string{" 12 ", "25", "30", "400"} {
				t.Setenv(names[i], value)
			}
			require.Equal(t, livelimits.Limits{Connections: 12, SubscriptionsPerConnection: 25, LiveSubscriptions: 30, ExecutionSubscriptions: 400}, LoadWithMode(mode).LiveLimits)
			// Embedded callers supplying partial configs get the same defaults,
			// without reading environment overrides a second time.
			cfg := (&Config{Mode: mode, AppDataDir: t.TempDir(), LiveLimits: livelimits.Limits{Connections: 7}}).NormalizeForMode()
			expected := defaults
			expected.Connections = 7
			require.Equal(t, expected, cfg.LiveLimits)
		})
	}
}
