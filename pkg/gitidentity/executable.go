package gitidentity

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var isolatedGit struct {
	sync.Once
	path string
	err  error
}

// Executable isolates Git's home directory as well as its config. Git/libcurl
// otherwise consults the server user's .netrc even with credential helpers off.
// The wrapper contains no credentials; credentials remain process-local env.
func Executable(ctx context.Context) (string, error) {
	if !Configured(ctx) {
		return exec.LookPath("git")
	}
	isolatedGit.Do(func() {
		realGit, err := exec.LookPath("git")
		if err != nil {
			isolatedGit.err = err
			return
		}
		dir, err := os.MkdirTemp("", "ov-personal-git-")
		if err != nil {
			isolatedGit.err = err
			return
		}
		home := filepath.Join(dir, "home")
		if err = os.Mkdir(home, 0700); err != nil {
			os.RemoveAll(dir)
			isolatedGit.err = err
			return
		}
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
		script := "#!/bin/sh\nHOME=" + quote(home) + "\nexport HOME\nexec " + quote(realGit) + " \"$@\"\n"
		path := filepath.Join(dir, "git")
		if runtime.GOOS == "windows" {
			path += ".cmd"
			script = "@echo off\r\nset \"HOME=" + home + "\"\r\n\"" + realGit + "\" %*\r\n"
		}
		if err = os.WriteFile(path, []byte(script), 0700); err != nil {
			os.RemoveAll(dir)
			isolatedGit.err = err
			return
		}
		isolatedGit.path = path
	})
	if isolatedGit.err != nil {
		return "", fmt.Errorf("prepare isolated Git environment: %w", isolatedGit.err)
	}
	return isolatedGit.path, nil
}
