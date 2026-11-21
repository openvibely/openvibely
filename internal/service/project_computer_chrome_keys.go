package service

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"os/exec"
	"runtime"

	"golang.org/x/crypto/pbkdf2"
)

type chromeCookieDecrypt func([]byte, string, int) (string, error)

// Keys live only for this import. Looking up a key is deferred until a selected,
// unexpired cookie actually needs it; basic Linux storage never needs a keyring.
func chromeDecryptor(ctx context.Context) (chromeCookieDecrypt, func()) {
	return newChromeDecryptor(runtime.GOOS, func(accept func([]byte) bool) ([]byte, error) {
		switch runtime.GOOS {
		case "darwin":
			secret, err := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-w", "-s", "Chrome Safe Storage", "-a", "Chrome").Output()
			if err != nil {
				clear(secret)
				return nil, errors.New("Chrome Keychain access was not granted")
			}
			return bytes.TrimSuffix(secret, []byte("\n")), nil
		case "linux":
			return linuxChromeSecret(ctx, accept)
		default:
			return nil, errors.New("Chrome sign-in import is supported on local macOS and Linux only")
		}
	})
}

func newChromeDecryptor(goos string, secret func(func([]byte) bool) ([]byte, error)) (chromeCookieDecrypt, func()) {
	keys := map[string][][]byte{}
	decrypt := func(encrypted []byte, host string, version int) (string, error) {
		if len(encrypted) < 3 {
			return "", errors.New("Chrome cookie encryption is invalid")
		}
		prefix := string(encrypted[:3])
		if (goos != "linux" && goos != "darwin") || (prefix != "v10" && (goos != "linux" || prefix != "v11")) {
			return "", errors.New("This Chrome cookie uses unsupported encryption")
		}
		// A profile can retain cookies encrypted with different keys after changing
		// keyring backends. Keep keys that actually worked, only for this import.
		for _, key := range keys[prefix] {
			if value, err := decryptChromeCookie(encrypted, key, host, version); err == nil {
				return value, nil
			}
		}
		iterations := 1003
		if goos == "linux" {
			iterations = 1
		}
		derive := func(password []byte) []byte {
			return pbkdf2.Key(password, []byte("saltysalt"), iterations, 16, sha1.New)
		}
		var password []byte
		var err error
		if goos == "linux" && prefix == "v10" {
			password = []byte("peanuts")
		} else {
			password, err = secret(func(candidate []byte) bool {
				key := derive(candidate)
				defer clear(key)
				_, err := decryptChromeCookie(encrypted, key, host, version)
				return err == nil
			})
		}
		if err != nil {
			clear(password)
			return "", err
		}
		key := derive(password)
		clear(password)
		value, err := decryptChromeCookie(encrypted, key, host, version)
		if err != nil {
			clear(key)
			return "", errors.New("Could not decrypt a selected Chrome cookie with the available keyring keys")
		}
		keys[prefix] = append(keys[prefix], key)
		return value, nil
	}
	return decrypt, func() {
		for _, candidates := range keys {
			for _, key := range candidates {
				clear(key)
			}
		}
	}
}
