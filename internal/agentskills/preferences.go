package agentskills

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SkillEnabledPreference reads a user preference stored outside the skill package.
// Missing or invalid preferences leave the declaration's default in effect.
func SkillEnabledPreference(skillPath string) *bool {
	path := filepath.Join(filepath.Dir(filepath.Dir(skillPath)), ".preferences", strings.ToLower(filepath.Base(filepath.Dir(skillPath)))+".enabled")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(string(data)))
	if err != nil {
		return nil
	}
	return &enabled
}

// SetSkillEnabledPreference persists a standalone skill preference without
// changing app-owned files. Atomic replacement preserves it across startup sync.
func SetSkillEnabledPreference(root, key string, enabled bool) error {
	if root == "" || !isValidSlug(key) {
		return fmt.Errorf("invalid skill preference target")
	}
	dir := filepath.Join(root, SkillsDir, ".preferences")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".enabled-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(strconv.FormatBool(enabled) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, strings.ToLower(key)+".enabled"))
}
