package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const chromeKeyringHelp = "Could not read Chrome's Linux keyring. Run OpenVibely as the same desktop user as Chrome, unlock GNOME Keyring or KWallet, and retry. A desktop D-Bus session must be available."

type chromeKeyringCall func(destination string, path dbus.ObjectPath, method string, args ...any) ([]any, error)

func linuxChromeSecret(parent context.Context, accept func([]byte) bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, errors.New(chromeKeyringHelp)
	}
	defer conn.Close()
	call := func(dest string, path dbus.ObjectPath, method string, args ...any) ([]any, error) {
		result := conn.Object(dest, path).CallWithContext(ctx, method, 0, args...)
		return result.Body, result.Err
	}
	prompt := func(path dbus.ObjectPath) error {
		signals := make(chan *dbus.Signal, 8)
		conn.Signal(signals)
		defer conn.RemoveSignal(signals)
		opts := []dbus.MatchOption{dbus.WithMatchSender("org.freedesktop.secrets"), dbus.WithMatchObjectPath(path), dbus.WithMatchInterface("org.freedesktop.Secret.Prompt"), dbus.WithMatchMember("Completed")}
		if err := conn.AddMatchSignalContext(ctx, opts...); err != nil {
			return err
		}
		defer conn.RemoveMatchSignal(opts...)
		if _, err := call("org.freedesktop.secrets", path, "org.freedesktop.Secret.Prompt.Prompt", ""); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case sig, ok := <-signals:
				if !ok {
					return errors.New("keyring disconnected")
				}
				if sig.Path != path || sig.Name != "org.freedesktop.Secret.Prompt.Completed" {
					continue
				}
				var dismissed bool
				var result dbus.Variant
				if err := dbus.Store(sig.Body, &dismissed, &result); err != nil {
					return err
				}
				if dismissed {
					return errors.New("keyring unlock was declined")
				}
				return nil
			}
		}
	}
	// Match the desktop's usual Chrome backend, then try the other backend if
	// Chrome's key is absent or cannot decrypt the selected cookie. Never create, overwrite, or delete keyring entries.
	readers := []func() ([]byte, error){func() ([]byte, error) { return chromeSecretService(call, prompt) }, func() ([]byte, error) { return chromeKWallet(call) }}
	if strings.Contains(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), "KDE") {
		readers[0], readers[1] = readers[1], readers[0]
	}
	foundKey := false
	for _, read := range readers {
		value, err := read()
		if err == nil && len(value) > 0 {
			foundKey = true
			if accept(value) {
				return value, nil
			}
		}
		clear(value)
		if ctx.Err() != nil {
			break
		}
	}
	if foundKey {
		return nil, errors.New("Could not decrypt a selected Chrome cookie with the available Linux keyring keys")
	}
	return nil, errors.New(chromeKeyringHelp)
}

type chromeSecretValue struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

func chromeSecretService(call chromeKeyringCall, prompt func(dbus.ObjectPath) error) ([]byte, error) {
	const destination = "org.freedesktop.secrets"
	const service = "org.freedesktop.Secret.Service."
	const root dbus.ObjectPath = "/org/freedesktop/secrets"
	body, err := call(destination, root, service+"SearchItems", map[string]string{"application": "chrome"})
	if err != nil {
		return nil, err
	}
	var unlocked, locked []dbus.ObjectPath
	if err = dbus.Store(body, &unlocked, &locked); err != nil {
		return nil, err
	}
	if len(unlocked) == 0 && len(locked) > 0 {
		body, err = call(destination, root, service+"Unlock", locked)
		if err != nil {
			return nil, err
		}
		var path dbus.ObjectPath
		if err = dbus.Store(body, &unlocked, &path); err != nil {
			return nil, err
		}
		if path != "/" {
			if err = prompt(path); err != nil {
				return nil, err
			}
			body, err = call(destination, root, service+"SearchItems", map[string]string{"application": "chrome"})
			if err != nil {
				return nil, err
			}
			if err = dbus.Store(body, &unlocked, &locked); err != nil {
				return nil, err
			}
		}
	}
	if len(unlocked) == 0 {
		return nil, errors.New("Chrome key not found in Secret Service")
	}
	body, err = call(destination, root, service+"OpenSession", "plain", dbus.MakeVariant(""))
	if err != nil {
		return nil, err
	}
	var output dbus.Variant
	var session dbus.ObjectPath
	if err = dbus.Store(body, &output, &session); err != nil {
		return nil, err
	}
	defer call(destination, session, "org.freedesktop.Secret.Session.Close")
	body, err = call(destination, unlocked[0], "org.freedesktop.Secret.Item.GetSecret", session)
	if err != nil {
		return nil, err
	}
	var secret chromeSecretValue
	if err = dbus.Store(body, &secret); err != nil {
		return nil, err
	}
	if secret.Session != session || len(secret.Parameters) != 0 {
		clear(secret.Value)
		return nil, errors.New("invalid keyring session")
	}
	return secret.Value, nil
}

func chromeKWallet(call chromeKeyringCall) ([]byte, error) {
	const iface = "org.kde.KWallet."
	const app = "OpenVibely"
	for _, suffix := range []string{"6", "5", ""} {
		dest := "org.kde.kwalletd" + suffix
		path := dbus.ObjectPath("/modules/kwalletd" + suffix)
		body, err := call(dest, path, iface+"networkWallet")
		var wallet string
		if err != nil || dbus.Store(body, &wallet) != nil || wallet == "" {
			continue
		}
		body, err = call(dest, path, iface+"open", wallet, int64(0), app)
		var handle int32
		if err != nil || dbus.Store(body, &handle) != nil || handle < 0 {
			continue
		}
		value, readErr := func() ([]byte, error) {
			defer call(dest, path, iface+"close", handle, false, app)
			body, err := call(dest, path, iface+"hasEntry", handle, "Chrome Keys", "Chrome Safe Storage", app)
			var exists bool
			if err != nil {
				return nil, err
			}
			if err = dbus.Store(body, &exists); err != nil {
				return nil, err
			}
			if !exists {
				return nil, errors.New("Chrome key not found in wallet")
			}
			body, err = call(dest, path, iface+"readPassword", handle, "Chrome Keys", "Chrome Safe Storage", app)
			if err != nil {
				return nil, err
			}
			var password string
			if err = dbus.Store(body, &password); err != nil {
				return nil, err
			}
			return []byte(password), nil
		}()
		if readErr == nil && len(value) > 0 {
			return value, nil
		}
		clear(value)
	}
	return nil, errors.New("Chrome key not found in KWallet")
}
