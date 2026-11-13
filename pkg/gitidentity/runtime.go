package gitidentity

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Prepare installs the embedded wrapper at a stable absolute path. Identical
// content is reused; changed files are replaced atomically. Instances sharing
// a data directory also share the most recently installed wrapper version.
func Prepare(destination string) (*Runtime, error) {
	destination, err := filepath.Abs(destination)
	if err != nil {
		return nil, err
	}
	if err := install(destination); err != nil {
		return nil, fmt.Errorf("prepare Git wrapper: %w", err)
	}
	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.cmd"
	}
	return &Runtime{path: filepath.Join(destination, "bin", name)}, nil
}

func install(destination string) error {
	if err := os.MkdirAll(filepath.Join(destination, "bin"), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(destination, "home"), 0700); err != nil {
		return err
	}
	posix := `#!/bin/sh
real_git=${OV_GIT_REAL_EXECUTABLE:?missing Git executable}
` + identityFallbackScript + `
HOME=$(CDPATH= cd -- "${0%/*}/../home" && pwd) || exit 1
export HOME
` + networkScopeScript + `
exec "$real_git" "$@"
`
	windows := `$realGit = $env:OV_GIT_REAL_EXECUTABLE
if (-not $realGit) { throw 'missing Git executable' }
` + windowsIdentityFallbackScript + `
$env:HOME = Join-Path (Split-Path $PSScriptRoot -Parent) 'home'
` + windowsNetworkScopeScript
	for name, script := range map[string]string{
		"git":             posix,
		"git-wrapper.ps1": windows,
		"git.cmd":         "@echo off\r\npowershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File \"%~dp0git-wrapper.ps1\" %*\r\nexit /b %errorlevel%\r\n",
	} {
		if err := writeChanged(filepath.Join(destination, "bin", name), []byte(script)); err != nil {
			return err
		}
	}
	return nil
}

// Never truncate a wrapper another process may be executing.
func writeChanged(path string, content []byte) error {
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, content) {
		return os.Chmod(path, 0700)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".git-wrapper-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err = file.Chmod(0700); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return replaceFile(file.Name(), path)
}
