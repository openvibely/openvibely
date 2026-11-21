package service

import (
	"bufio"
	"context"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/require"
)

// Starts a private bus and exports fixture keyrings. This must never connect to
// the developer's desktop bus or read a real Chrome secret.
func TestChromeLinuxPrivateKeyringBus(t *testing.T) {
	binary, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon unavailable; run in isolated Linux container")
	}
	for _, tc := range []struct {
		name, desktop, secret, wallet string
		locked, wantError             bool
	}{
		{name: "unlocked", desktop: "GNOME", secret: "fixture-only-key"},
		{name: "unlock-prompt", desktop: "GNOME", secret: "fixture-only-key", locked: true},
		{name: "stale-wallet", desktop: "KDE", secret: "fixture-only-key", wallet: "old-wallet-key"},
		{name: "stale-secret-service", desktop: "GNOME", secret: "old-secret-key", wallet: "fixture-only-key"},
		{name: "stale-wallet-unlock-secret-service", desktop: "KDE", secret: "fixture-only-key", wallet: "old-wallet-key", locked: true},
		{name: "neither-key-matches", desktop: "GNOME", secret: "old-secret-key", wallet: "old-wallet-key", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "--session", "--nofork", "--print-address=1")
			out, err := cmd.StdoutPipe()
			require.NoError(t, err)
			require.NoError(t, cmd.Start())
			defer func() { cmd.Process.Kill(); cmd.Wait() }()
			scanner := bufio.NewScanner(out)
			require.True(t, scanner.Scan())
			t.Setenv("DBUS_SESSION_BUS_ADDRESS", scanner.Text())
			t.Setenv("XDG_CURRENT_DESKTOP", tc.desktop)
			var reads atomic.Int32
			conn, err := dbus.ConnectSessionBus()
			require.NoError(t, err)
			defer conn.Close()
			_, err = conn.RequestName("org.freedesktop.secrets", dbus.NameFlagDoNotQueue)
			require.NoError(t, err)
			const item dbus.ObjectPath = "/fixture/item"
			const session dbus.ObjectPath = "/fixture/session"
			const prompt dbus.ObjectPath = "/fixture/prompt"
			// Tests are sequential; D-Bus dispatch may run methods concurrently.
			unlocked := make(chan struct{})
			if !tc.locked {
				close(unlocked)
			}
			require.NoError(t, conn.ExportMethodTable(map[string]interface{}{
				"SearchItems": func(attrs map[string]string) ([]dbus.ObjectPath, []dbus.ObjectPath, *dbus.Error) {
					if attrs["application"] != "chrome" {
						return nil, nil, dbus.MakeFailedError(context.Canceled)
					}
					select {
					case <-unlocked:
						return []dbus.ObjectPath{item}, []dbus.ObjectPath{}, nil
					default:
						return []dbus.ObjectPath{}, []dbus.ObjectPath{item}, nil
					}
				},
				"Unlock": func(paths []dbus.ObjectPath) ([]dbus.ObjectPath, dbus.ObjectPath, *dbus.Error) {
					return []dbus.ObjectPath{}, prompt, nil
				},
				"OpenSession": func(algorithm string, input dbus.Variant) (dbus.Variant, dbus.ObjectPath, *dbus.Error) {
					return dbus.MakeVariant(""), session, nil
				},
			}, "/org/freedesktop/secrets", "org.freedesktop.Secret.Service"))
			require.NoError(t, conn.ExportMethodTable(map[string]interface{}{
				"Prompt": func(window string) *dbus.Error {
					close(unlocked)
					conn.Emit(prompt, "org.freedesktop.Secret.Prompt.Completed", false, dbus.MakeVariant([]dbus.ObjectPath{item}))
					return nil
				},
			}, prompt, "org.freedesktop.Secret.Prompt"))
			require.NoError(t, conn.ExportMethodTable(map[string]interface{}{
				"GetSecret": func(s dbus.ObjectPath) (chromeSecretValue, *dbus.Error) {
					reads.Add(1)
					return chromeSecretValue{Session: s, Parameters: []byte{}, Value: []byte(tc.secret), ContentType: "text/plain"}, nil
				},
			}, item, "org.freedesktop.Secret.Item"))
			require.NoError(t, conn.ExportMethodTable(map[string]interface{}{"Close": func() *dbus.Error { return nil }}, session, "org.freedesktop.Secret.Session"))
			if tc.wallet != "" {
				_, err = conn.RequestName("org.kde.kwalletd6", dbus.NameFlagDoNotQueue)
				require.NoError(t, err)
				require.NoError(t, conn.ExportMethodTable(map[string]interface{}{
					"networkWallet": func() (string, *dbus.Error) { return "kdewallet", nil },
					"open":          func(string, int64, string) (int32, *dbus.Error) { return 7, nil },
					"hasEntry":      func(int32, string, string, string) (bool, *dbus.Error) { return true, nil },
					"readPassword":  func(int32, string, string, string) (string, *dbus.Error) { reads.Add(1); return tc.wallet, nil },
					"close":         func(int32, bool, string) (int32, *dbus.Error) { return 0, nil },
				}, "/modules/kwalletd6", "org.kde.KWallet"))
			}
			decrypt, cleanup := newChromeDecryptor("linux", func(accept func([]byte) bool) ([]byte, error) { return linuxChromeSecret(ctx, accept) })
			defer cleanup()
			encrypted := fixtureChromeCookie(t, "v11", "fixture-only-key", "example.test", "fixture-cookie", 1, 24)
			value, err := decrypt(encrypted, "example.test", 24)
			if tc.wantError {
				require.ErrorContains(t, err, "available Linux keyring keys")
				require.Empty(t, value)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "fixture-cookie", value)
			before := reads.Load()
			require.Positive(t, before)
			value, err = decrypt(encrypted, "example.test", 24)
			require.NoError(t, err)
			require.Equal(t, "fixture-cookie", value)
			require.Equal(t, before, reads.Load(), "reuse verified key without prompting again")
		})
	}
}
