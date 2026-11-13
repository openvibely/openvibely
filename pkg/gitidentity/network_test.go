package gitidentity

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestNetworkCredentialsOverrideSavedRepositoryHeaders(t *testing.T) {
	var mu sync.Mutex
	var received []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received = append(received, r.Header.Values("Authorization")...)
		mu.Unlock()
		http.Error(w, "fixture repository", http.StatusNotFound)
	}))
	defer server.Close()
	endpoint := server.URL + "/acme/app.git"
	u, _ := url.Parse(server.URL)
	ctx := testResolver(context.Background(), func(context.Context) (Identity, error) {
		return Identity{Name: "Alice", Email: "alice@example.com", Host: u.Host, Token: "alice-token"}, nil
	})
	dir := t.TempDir()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, context.Background(), "init")
	gitRun(t, dir, context.Background(), "config", "http.sslCAInfo", ca)
	gitRun(t, dir, context.Background(), "remote", "add", "origin", endpoint)
	gitRun(t, dir, ctx, "commit", "--allow-empty", "-m", "initial")
	for _, scope := range []string{server.URL + "/", server.URL + "/acme/", endpoint} {
		gitRun(t, dir, context.Background(), "config", "--add", "http."+scope+".extraHeader", "Authorization: Basic bob-token")
	}
	before, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
	want := "AUTHORIZATION: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:alice-token"))
	for _, operation := range []string{"clone", "fetch", "push", "agent-push", "plugin-pull"} {
		t.Run(operation, func(t *testing.T) {
			mu.Lock()
			received = nil
			mu.Unlock()
			var cmd *exec.Cmd
			switch operation {
			case "clone":
				cmd = Command(ctx, "-c", "http.sslCAInfo="+ca, "clone", endpoint, filepath.Join(t.TempDir(), "clone"))
			case "fetch":
				cmd = Command(ctx, "fetch", endpoint)
			case "push":
				cmd = Command(ctx, "push", endpoint, "HEAD:main")
			default:
				line := "git push origin HEAD:main"
				if operation == "plugin-pull" {
					line = "git -C '" + dir + "' pull --ff-only"
				}
				if runtime.GOOS == "windows" {
					if operation == "plugin-pull" {
						line = `git -C "` + dir + `" pull --ff-only`
					}
					cmd = exec.Command("cmd.exe", "/d", "/c", line)
				} else {
					cmd = exec.Command("/bin/sh", "-c", line)
				}
				var err error
				cmd.Env, err = Environment(ctx, os.Environ())
				if err != nil {
					t.Fatal(err)
				}
			}
			cmd.Dir = dir
			out, _ := cmd.CombinedOutput() // The fixture deliberately rejects the Git protocol.
			mu.Lock()
			headers := append([]string(nil), received...)
			mu.Unlock()
			if len(headers) == 0 {
				t.Fatalf("no network request: %s", out)
			}
			for _, header := range headers {
				if !strings.EqualFold("Authorization: "+header, want) {
					t.Fatalf("wrong authorization header (not disclosing credential)")
				}
			}
			after, _ := os.ReadFile(filepath.Join(dir, ".git", "config"))
			if string(before) != string(after) {
				t.Fatal("repository config changed")
			}
		})
	}
}
