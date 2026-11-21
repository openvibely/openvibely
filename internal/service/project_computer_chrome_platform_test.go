package service

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/chromefixture"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/pbkdf2"
)

func fixtureChromeCookie(t *testing.T, prefix, password, host, value string, iterations, version int) []byte {
	t.Helper()
	plain := []byte(value)
	if version >= 24 {
		hash := sha256.Sum256([]byte(host))
		plain = append(hash[:], plain...)
	}
	pad := 16 - len(plain)%16
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	key := pbkdf2.Key([]byte(password), []byte("saltysalt"), iterations, 16, sha1.New)
	block, err := aes.NewCipher(key)
	require.NoError(t, err)
	cipher.NewCBCEncrypter(block, bytes.Repeat([]byte{32}, 16)).CryptBlocks(plain, plain)
	return append([]byte(prefix), plain...)
}

func TestChromeLinuxDecryptionAndConsent(t *testing.T) {
	db, err := chromefixture.Open(filepath.Join(t.TempDir(), "cookies.db"))
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE meta(key TEXT,value TEXT);INSERT INTO meta VALUES('version','24');CREATE TABLE cookies(host_key TEXT,name TEXT,value TEXT,encrypted_value BLOB,path TEXT,expires_utc INTEGER,is_secure INTEGER,is_httponly INTEGER,samesite INTEGER,top_frame_site_key TEXT);`)
	require.NoError(t, err)
	host := "example.test"
	for _, row := range []struct{ name, prefix, password string }{{"basic", "v10", "peanuts"}, {"keyring", "v11", "fixture-keyring"}, {"excluded", "v20", "unsupported"}} {
		_, err = db.Exec(`INSERT INTO cookies VALUES(?,?,?,?,'/',0,1,1,1,'')`, host, row.name, "", fixtureChromeCookie(t, row.prefix, row.password, host, row.name, 1, 24))
		require.NoError(t, err)
	}
	calls := 0
	decrypt, cleanup := newChromeDecryptor("linux", func(_ func([]byte) bool) ([]byte, error) { calls++; return []byte("fixture-keyring"), nil })
	defer cleanup()
	pick := func(name string) models.ChromeCookieSelection {
		return models.ChromeCookieSelection{Profile: "Default", Domain: host, Name: name, Path: "/"}
	}
	cookies, err := selectedChromeCookiesWithDecrypt(t.Context(), db, decrypt, []string{host}, pick("basic"))
	require.NoError(t, err)
	require.Len(t, cookies, 1)
	require.Equal(t, "basic", cookies[0]["value"])
	require.Zero(t, calls)
	cookies, err = selectedChromeCookiesWithDecrypt(t.Context(), db, decrypt, []string{host}, pick("basic"), pick("keyring"))
	require.NoError(t, err)
	require.Len(t, cookies, 2)
	require.Equal(t, 1, calls)
	_, err = selectedChromeCookiesWithDecrypt(t.Context(), db, decrypt, []string{host}, pick("excluded"))
	require.ErrorContains(t, err, "unsupported")
	_, err = decrypt(fixtureChromeCookie(t, "v11", "fixture-keyring", host, "secret", 1, 24), "different.test", 24)
	require.Error(t, err)
	denied, clean := newChromeDecryptor("linux", func(_ func([]byte) bool) ([]byte, error) { return nil, errors.New("Unlock fixture keyring") })
	defer clean()
	_, err = selectedChromeCookiesWithDecrypt(t.Context(), db, denied, []string{host}, pick("basic"), pick("keyring"))
	require.ErrorContains(t, err, "Unlock fixture keyring")
}

func TestChromeMacDecryptionUnchanged(t *testing.T) {
	decrypt, cleanup := newChromeDecryptor("darwin", func(_ func([]byte) bool) ([]byte, error) { return []byte("mac-fixture"), nil })
	defer cleanup()
	for _, version := range []int{23, 24} {
		value, err := decrypt(fixtureChromeCookie(t, "v10", "mac-fixture", "example.test", "cookie", 1003, version), "example.test", version)
		require.NoError(t, err)
		require.Equal(t, "cookie", value)
	}
	_, err := decrypt(fixtureChromeCookie(t, "v11", "mac-fixture", "example.test", "cookie", 1, 24), "example.test", 24)
	require.ErrorContains(t, err, "unsupported")
}

func TestChromeLinuxProfilePaths(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{nil, filepath.Join(home, ".config", "google-chrome")},
		{map[string]string{"XDG_CONFIG_HOME": filepath.Join(home, "xdg")}, filepath.Join(home, "xdg", "google-chrome")},
		{map[string]string{"CHROME_CONFIG_HOME": filepath.Join(home, "chrome"), "XDG_CONFIG_HOME": filepath.Join(home, "xdg")}, filepath.Join(home, "chrome", "google-chrome")},
		{map[string]string{"CHROME_USER_DATA_DIR": filepath.Join(home, "profile")}, filepath.Join(home, "profile")},
		{map[string]string{"XDG_CONFIG_HOME": "relative"}, filepath.Join(home, ".config", "google-chrome")},
	} {
		require.Equal(t, tc.want, chromeProfileRoot("linux", home, func(k string) string { return tc.env[k] }))
	}
}

func TestChromeKWalletReadOnly(t *testing.T) {
	closed := false
	call := func(dest string, path dbus.ObjectPath, method string, args ...any) ([]any, error) {
		require.Equal(t, "org.kde.kwalletd6", dest)
		require.Equal(t, dbus.ObjectPath("/modules/kwalletd6"), path)
		switch method {
		case "org.kde.KWallet.networkWallet":
			return []any{"kdewallet"}, nil
		case "org.kde.KWallet.open":
			require.Equal(t, []any{"kdewallet", int64(0), "OpenVibely"}, args)
			return []any{int32(8)}, nil
		case "org.kde.KWallet.hasEntry":
			require.Equal(t, []any{int32(8), "Chrome Keys", "Chrome Safe Storage", "OpenVibely"}, args)
			return []any{true}, nil
		case "org.kde.KWallet.readPassword":
			return []any{"wallet-secret"}, nil
		case "org.kde.KWallet.close":
			closed = true
			require.Equal(t, []any{int32(8), false, "OpenVibely"}, args)
			return []any{int32(0)}, nil
		default:
			t.Fatalf("unexpected wallet operation: %s", method)
			return nil, errors.New("unexpected")
		}
	}
	value, err := chromeKWallet(call)
	require.NoError(t, err)
	require.Equal(t, "wallet-secret", string(value))
	require.True(t, closed)
}

func TestChromeLinuxCachesBothWorkingKeys(t *testing.T) {
	calls := 0
	decrypt, cleanup := newChromeDecryptor("linux", func(accept func([]byte) bool) ([]byte, error) {
		calls++
		for _, password := range []string{"key-one", "key-two"} {
			candidate := []byte(password)
			if accept(candidate) {
				return candidate, nil
			}
			clear(candidate)
		}
		return nil, errors.New("no matching key")
	})
	defer cleanup()
	for _, password := range []string{"key-one", "key-two", "key-one", "key-two"} {
		value, err := decrypt(fixtureChromeCookie(t, "v11", password, "example.test", password, 1, 24), "example.test", 24)
		require.NoError(t, err)
		require.Equal(t, password, value)
	}
	require.Equal(t, 2, calls)
}
