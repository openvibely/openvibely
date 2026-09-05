//go:build linux

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	desktopicons "github.com/openvibely/openvibely/assets/desktop/icons"
)

const installDesktopFlag = "--install-desktop"

func handleDesktopIntegrationCommand(args []string) (bool, error) {
	if len(args) != 2 || args[1] != installDesktopFlag {
		return false, nil
	}
	return true, installLinuxDesktopIntegration()
}

func installLinuxDesktopIntegration() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	dataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	binHome := strings.TrimSpace(os.Getenv("XDG_BIN_HOME"))
	if binHome == "" {
		binHome = filepath.Join(home, ".local", "bin")
	}

	sourceExecutable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate OpenVibely executable: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(sourceExecutable); resolveErr == nil {
		sourceExecutable = resolved
	}
	destinationExecutable := filepath.Join(binHome, "openvibely-desktop")
	desktopPath := filepath.Join(dataHome, "applications", "com.openvibely.desktop.desktop")
	iconThemePath := filepath.Join(dataHome, "icons", "hicolor")
	escapedExecutable, err := escapeDesktopExec(destinationExecutable)
	if err != nil {
		return fmt.Errorf("prepare application-menu entry: %w", err)
	}
	desktopEntry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=OpenVibely
Comment=OpenVibely desktop application
Exec="%s"
Icon=com.openvibely.desktop
Terminal=false
Categories=Development;Utility;
StartupWMClass=com.openvibely.desktop
`, escapedExecutable)

	if filepath.Clean(sourceExecutable) != filepath.Clean(destinationExecutable) {
		if err := copyLinuxDesktopFile(sourceExecutable, destinationExecutable, 0o755); err != nil {
			return fmt.Errorf("install OpenVibely executable: %w", err)
		}
	}

	for _, size := range []string{"16", "24", "32", "48", "64", "128", "256", "512", "1024"} {
		icon, readErr := desktopicons.LinuxIconFiles.ReadFile("linux/" + size + "x" + size + ".png")
		if readErr != nil {
			return fmt.Errorf("read %spx application icon: %w", size, readErr)
		}
		iconPath := filepath.Join(iconThemePath, size+"x"+size, "apps", "com.openvibely.desktop.png")
		if err := writeLinuxDesktopFile(iconPath, icon, 0o644); err != nil {
			return fmt.Errorf("install %spx application icon: %w", size, err)
		}
	}

	if err := writeLinuxDesktopFile(desktopPath, []byte(desktopEntry), 0o644); err != nil {
		return fmt.Errorf("install application-menu entry: %w", err)
	}

	var iconCacheErr error
	if updateIconCache, lookupErr := exec.LookPath("gtk-update-icon-cache"); lookupErr == nil {
		if err := exec.Command(updateIconCache, "-q", "-t", "-f", iconThemePath).Run(); err != nil {
			iconCacheErr = fmt.Errorf("refresh application icon cache: %w", err)
		}
	}
	if updateDatabase, lookupErr := exec.LookPath("update-desktop-database"); lookupErr == nil {
		_ = exec.Command(updateDatabase, filepath.Dir(desktopPath)).Run()
	}
	if iconCacheErr != nil {
		return iconCacheErr
	}
	fmt.Printf("Installed OpenVibely desktop integration in %s\n", dataHome)
	return nil
}

func escapeDesktopExec(value string) (string, error) {
	for _, character := range value {
		if character == '=' || unicode.IsControl(character) {
			return "", fmt.Errorf("executable path contains unsupported character %q", character)
		}
	}
	replacer := strings.NewReplacer(`\`, `\\\\`, `"`, `\"`, "`", "\\`", `$`, `\\$`, `%`, `%%`)
	return replacer.Replace(value), nil
}

func copyLinuxDesktopFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	return publishLinuxDesktopFile(destination, mode, func(output *os.File) error {
		_, err := io.Copy(output, input)
		return err
	})
}

func writeLinuxDesktopFile(destination string, data []byte, mode os.FileMode) error {
	return publishLinuxDesktopFile(destination, mode, func(output *os.File) error {
		_, err := output.Write(data)
		return err
	})
}

func publishLinuxDesktopFile(destination string, mode os.FileMode, write func(*os.File) error) (err error) {
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".openvibely-install-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := write(temporary); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, destination)
}
