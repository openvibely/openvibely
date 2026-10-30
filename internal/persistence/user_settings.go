package persistence

import (
	"context"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"strings"
)

// UserSettings routes presentation preferences through the current principal.
// Non-request/background callers retain the bootstrap owner's preferences.
// Integration settings remain instance-scoped until the credentials phase.
type UserSettings struct {
	SettingsStore
	Organizations OrganizationStore
}

func preferenceUser(ctx context.Context) string {
	if p, ok := models.PrincipalFromContext(ctx); ok && p.UserID != "" {
		return p.UserID
	}
	return models.BootstrapUserID
}
func (s *UserSettings) Get(ctx context.Context, key string) (string, error) {
	if !strings.HasPrefix(key, "ui.") {
		return s.SettingsStore.Get(ctx, key)
	}
	v, err := s.Organizations.Preferences(ctx, preferenceUser(ctx), []string{key})
	return v[key], err
}
func (s *UserSettings) GetMany(ctx context.Context, keys []string) (map[string]string, error) {
	var personal, shared []string
	for _, key := range keys {
		if strings.HasPrefix(key, "ui.") {
			personal = append(personal, key)
		} else {
			shared = append(shared, key)
		}
	}
	out, err := s.SettingsStore.GetMany(ctx, shared)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]string{}
	}
	prefs, err := s.Organizations.Preferences(ctx, preferenceUser(ctx), personal)
	if err != nil {
		return nil, err
	}
	for k, v := range prefs {
		out[k] = v
	}
	return out, nil
}
func (s *UserSettings) Set(ctx context.Context, key, value string) error {
	if !strings.HasPrefix(key, "ui.") {
		return s.SettingsStore.Set(ctx, key, value)
	}
	return s.Organizations.SetPreferences(ctx, preferenceUser(ctx), map[string]string{key: value})
}
func (s *UserSettings) SetMany(ctx context.Context, values map[string]string) error {
	if personal, shared := settingsScope(values); personal && shared {
		return errors.New("cannot atomically mix personal and shared settings")
	}
	personal, shared := map[string]string{}, map[string]string{}
	for k, v := range values {
		if strings.HasPrefix(k, "ui.") {
			personal[k] = v
		} else {
			shared[k] = v
		}
	}
	if err := s.SettingsStore.SetMany(ctx, shared); err != nil {
		return err
	}
	if len(personal) > 0 {
		return s.Organizations.SetPreferences(ctx, preferenceUser(ctx), personal)
	}
	return nil
}

// Atomic settings operations must stay entirely within personal or shared
// storage. Reject mixed operations rather than partially committing them.
func settingsScope(groups ...map[string]string) (personal, shared bool) {
	for _, group := range groups {
		for key := range group {
			if strings.HasPrefix(key, "ui.") {
				personal = true
			} else {
				shared = true
			}
		}
	}
	return
}
func (s *UserSettings) CompareAndSet(ctx context.Context, key, expected, value string, guards map[string]string) (bool, error) {
	return s.CompareAndSetMany(ctx, map[string]string{key: expected}, map[string]string{key: value}, guards)
}
func (s *UserSettings) CompareAndSetMany(ctx context.Context, expected, values, guards map[string]string) (bool, error) {
	personal, shared := settingsScope(expected, values, guards)
	if personal && shared {
		return false, errors.New("cannot atomically mix personal and shared settings")
	}
	if personal {
		return s.Organizations.ComparePreferences(ctx, preferenceUser(ctx), expected, values, guards)
	}
	return s.SettingsStore.CompareAndSetMany(ctx, expected, values, guards)
}
func (s *UserSettings) MatchesInTx(ctx context.Context, tx Tx, expected map[string]string) (bool, error) {
	if personal, _ := settingsScope(expected); personal {
		return false, errors.New("personal preferences cannot guard shared channel transactions")
	}
	return s.SettingsStore.MatchesInTx(ctx, tx, expected)
}
