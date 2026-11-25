// Package repokey identifies which repository a project is a copy of.
package repokey

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

// gitRemotes uses Git to honor quoting, includes, and worktree configuration.
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

// readGitRemotes bounds both config reads together. Missing repositories and unset
// remotes are empty results; malformed or inaccessible config remains an error
// so the background cache retains its previous identity and retries.
func readGitRemotes(repoPath string) (map[string]string, error) {
	if repoPath == "" {
		return nil, nil
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := map[string]string{}
	for _, name := range []string{"upstream", "origin"} {
		cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "config", "--includes", "--get", "remote."+name+".url")
		cmd.WaitDelay = time.Second
		data, err := cmd.Output()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				continue
			}
			return nil, err
		}
		out[name] = strings.TrimSpace(string(data))
		if Normalize(out[name]) != "" {
			break
		}
	}
	return out, nil
}
