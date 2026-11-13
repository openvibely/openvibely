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

// RequireCommit validates access to the acting user's connection. Missing author
// fields are resolved by Git in the command directory, not rejected here.
func RequireCommit(ctx context.Context) error {
	if !Configured(ctx) {
		return nil
	}
	_, err := Resolve(ctx)
	return err
}

// Environment clears ambient Git credentials before applying the
// caller's values. Missing identity fields retain legacy Git identity resolution.
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
		if (strings.HasPrefix(key, "OV_GIT_AUTH_") || strings.HasPrefix(key, "OV_GIT_IDENTITY_")) || strings.HasPrefix(key, "GIT_TRACE") || key == "GIT_CURL_VERBOSE" || key == "GIT_SSL_NO_VERIFY" || strings.HasPrefix(key, "GIT_CONFIG_") || key == "GH_TOKEN" || key == "GITHUB_TOKEN" || key == "GH_ENTERPRISE_TOKEN" || key == "GITHUB_ENTERPRISE_TOKEN" || key == "GIT_ASKPASS" || key == "SSH_ASKPASS" || key == "GIT_SSH" || key == "GIT_SSH_COMMAND" || key == "GIT_TERMINAL_PROMPT" || key == "GH_CONFIG_DIR" || key == "GH_HOST" {
			continue
		}
		if key == "PATH" {
			_, value, _ := strings.Cut(entry, "=")
			entry = "PATH=" + filepath.Dir(gitExecutable) + string(os.PathListSeparator) + value
		}
		out = append(out, entry)
	}
	out = append(out, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=true", "GIT_SSH_COMMAND=false", "GH_CONFIG_DIR="+os.DevNull)
	// The wrapper reads only identity from the original config, in the command's
	// working directory, before isolating HOME and all network credentials.
	for _, entry := range base {
		key, value, _ := strings.Cut(entry, "=")
		if key == "GIT_CONFIG_GLOBAL" || key == "GIT_CONFIG_SYSTEM" || key == "GIT_CONFIG_NOSYSTEM" {
			out = append(out, "OV_GIT_IDENTITY_"+key+"="+value)
		}
	}
	identityParameters, err := environmentIdentityConfig(ctx, base, gitExecutable)
	if err != nil {
		return nil, err
	}
	if identityParameters != "" {
		out = append(out, "OV_GIT_IDENTITY_PARAMETERS="+identityParameters)
	}
	for _, field := range []struct{ suffix, value string }{{"NAME", i.Name}, {"EMAIL", i.Email}} {
		if strings.TrimSpace(field.value) == "" {
			continue
		}
		for _, prefix := range []string{"GIT_AUTHOR_", "GIT_COMMITTER_"} {
			key := prefix + field.suffix
			filtered := out[:0]
			for _, entry := range out {
				if !strings.HasPrefix(entry, key+"=") {
					filtered = append(filtered, entry)
				}
			}
			out = append(filtered, key+"="+field.value)
		}
	}
	config := [][2]string{{"credential.helper", ""}, {"http.extraHeader", ""}, {"http.followRedirects", "false"}, {"http.sslVerify", "true"}}
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
