// Package gitidentity supplies Git identity and credentials to individual child
// processes. It never changes repository, user, or machine configuration.
package gitidentity

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type Identity struct{ Name, Email, Token, Host, Scope string }
type Resolver func(context.Context) (Identity, error)
type contextKey struct{}

func WithResolver(ctx context.Context, r Resolver) context.Context {
	return context.WithValue(ctx, contextKey{}, r)
}
func Configured(ctx context.Context) bool { _, ok := ctx.Value(contextKey{}).(Resolver); return ok }
func Resolve(ctx context.Context) (Identity, error) {
	r, ok := ctx.Value(contextKey{}).(Resolver)
	if !ok {
		return Identity{}, nil
	}
	return r(ctx)
}
func RequireCommit(ctx context.Context) error {
	if !Configured(ctx) {
		return nil
	}
	i, err := Resolve(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(i.Name) == "" || strings.TrimSpace(i.Email) == "" {
		return fmt.Errorf("configure your commit author and email in the GitHub channel settings")
	}
	return nil
}

// Environment clears ambient Git credentials and identity before applying the
// caller's values. Empty identity fields intentionally prevent config fallback.
// Rebase commands retain Git's original-author handling; the committer comes
// from this environment.
func Environment(ctx context.Context, base []string) ([]string, error) {
	return CommandEnvironment(ctx, base, nil)
}

// CommandEnvironment scopes credentials to repository URLs supplied to Git.
// Same-key empty values clear saved headers before adding the acting user's.
func CommandEnvironment(ctx context.Context, base []string, args []string) ([]string, error) {
	if !Configured(ctx) {
		return base, nil
	}
	i, err := Resolve(ctx)
	if err != nil {
		return nil, err
	}
	gitExecutable, err := Executable(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(base)+24)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "OV_GIT_AUTH_") || strings.HasPrefix(key, "GIT_TRACE") || key == "GIT_CURL_VERBOSE" || key == "GIT_SSL_NO_VERIFY" || strings.HasPrefix(key, "GIT_CONFIG_") || strings.HasPrefix(key, "GIT_AUTHOR_") || strings.HasPrefix(key, "GIT_COMMITTER_") || key == "EMAIL" || key == "GH_TOKEN" || key == "GITHUB_TOKEN" || key == "GH_ENTERPRISE_TOKEN" || key == "GITHUB_ENTERPRISE_TOKEN" || key == "GIT_ASKPASS" || key == "SSH_ASKPASS" || key == "GIT_SSH" || key == "GIT_SSH_COMMAND" || key == "GIT_TERMINAL_PROMPT" || key == "GH_CONFIG_DIR" || key == "GH_HOST" {
			continue
		}
		if key == "PATH" {
			_, value, _ := strings.Cut(entry, "=")
			entry = "PATH=" + filepath.Dir(gitExecutable) + string(os.PathListSeparator) + value
		}
		out = append(out, entry)
	}
	out = append(out, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=true", "GIT_SSH_COMMAND=false", "GH_CONFIG_DIR="+os.DevNull, "GIT_AUTHOR_NAME="+i.Name, "GIT_AUTHOR_EMAIL="+i.Email, "GIT_COMMITTER_NAME="+i.Name, "GIT_COMMITTER_EMAIL="+i.Email)
	config := [][2]string{{"credential.helper", ""}, {"http.extraHeader", ""}, {"user.useConfigOnly", "true"}, {"http.followRedirects", "false"}, {"http.sslVerify", "true"}}
	if i.Token != "" {
		u, err := url.Parse("https://" + i.Host)
		if err != nil || u.Host != i.Host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("invalid GitHub credential host")
		}
		if i.Host == "github.com" {
			out = append(out, "GH_TOKEN="+i.Token)
		} else {
			out = append(out, "GH_ENTERPRISE_TOKEN="+i.Token)
		}
		header := "AUTHORIZATION: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+i.Token))
		out = append(out, "GH_HOST="+i.Host, "OV_GIT_AUTH_HOST="+i.Host, "OV_GIT_AUTH_HEADER="+header)
		key := "http.https://" + i.Host + "/.extraHeader"
		config = append(config, [2]string{key, ""}, [2]string{key, header})
		for _, raw := range args {
			target, err := url.Parse(raw)
			if err != nil || target.Scheme != "https" || target.Host != i.Host || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
				continue
			}
			exact := "http." + target.String() + ".extraHeader"
			config = append(config, [2]string{exact, ""}, [2]string{exact, header})
		}
	}
	if runtime.GOOS == "windows" {
		// Windows PowerShell can discard empty environment values. Preserve
		// header/helper resets in Git's quoted parameter representation.
		return append(out, "GIT_CONFIG_PARAMETERS="+configParameters(config)), nil
	}
	out = append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
	for n, pair := range config {
		out = append(out, "GIT_CONFIG_KEY_"+strconv.Itoa(n)+"="+pair[0], "GIT_CONFIG_VALUE_"+strconv.Itoa(n)+"="+pair[1])
	}
	return out, nil
}

func configParameters(config [][2]string) string {
	parameters := make([]string, 0, len(config))
	for _, pair := range config {
		parameters = append(parameters, "'"+strings.ReplaceAll(pair[0]+"="+pair[1], "'", "'\\''")+"'")
	}
	return strings.Join(parameters, " ")
}
