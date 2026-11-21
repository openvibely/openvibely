// Package repokey identifies which repository a project is a copy of.
package repokey

import (
	"bufio"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Normalize reduces a git remote to host/path, e.g. github.com/acme/foo.
// Local paths and unparseable values have no key.
func Normalize(raw string) string {
	s := strings.TrimSpace(raw)
	var host, path string
	if i := strings.Index(s, "://"); i > 0 {
		u, err := url.Parse(s)
		if err != nil || u.Scheme == "file" {
			return ""
		}
		host, path = u.Hostname(), u.Path
	} else if at := strings.Index(s, "@"); at > 0 && strings.Contains(s[at:], ":") && !strings.Contains(s[:at], "/") {
		host, path, _ = strings.Cut(s[at+1:], ":")
	} else {
		return ""
	}
	host = strings.ToLower(strings.TrimSpace(host))
	path = strings.Trim(strings.TrimSuffix(strings.Trim(strings.TrimSpace(path), "/"), ".git"), "/")
	if host == "" || path == "" {
		return ""
	}
	return host + "/" + path
}

// For returns the key of a project: its saved repository URL, else the
// repository's upstream remote, else its origin remote.
func For(repoURL, repoPath string) string {
	if key := Normalize(repoURL); key != "" {
		return key
	}
	if repoPath == "" {
		return ""
	}
	remotes := gitRemotes(repoPath)
	if key := Normalize(remotes["upstream"]); key != "" {
		return key
	}
	return Normalize(remotes["origin"])
}

// gitRemotes reads remote URLs from the repository's config without running git.
func gitRemotes(repoPath string) map[string]string {
	remotes, _ := readGitRemotes(repoPath)
	return remotes
}

func resolveIdentity(repoURL, repoPath string) (string, error) {
	if key := Normalize(repoURL); key != "" {
		return key, nil
	}
	remotes, err := readGitRemotes(repoPath)
	if err != nil {
		return "", err
	}
	if key := Normalize(remotes["upstream"]); key != "" {
		return key, nil
	}
	return Normalize(remotes["origin"]), nil
}

func readGitRemotes(repoPath string) (map[string]string, error) {
	dir := filepath.Join(repoPath, ".git")
	if data, err := os.ReadFile(dir); err == nil {
		// A worktree's .git file points at its gitdir, whose commondir holds the config.
		gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
		if !ok {
			return nil, nil
		}
		dir = strings.TrimSpace(gitdir)
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(repoPath, dir)
		}
		if common, err := os.ReadFile(filepath.Join(dir, "commondir")); err == nil {
			c := strings.TrimSpace(string(common))
			if !filepath.IsAbs(c) {
				c = filepath.Join(dir, c)
			}
			dir = c
		}
	}
	f, err := os.Open(filepath.Join(dir, "config"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = ""
			if name, ok := strings.CutPrefix(strings.TrimSuffix(line, "]"), "[remote "); ok {
				section = strings.Trim(strings.TrimSpace(name), `"`)
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if section != "" && ok && strings.EqualFold(strings.TrimSpace(key), "url") {
			if _, seen := out[section]; !seen {
				out[section] = strings.TrimSpace(value)
			}
		}
	}
	return out, sc.Err()
}
