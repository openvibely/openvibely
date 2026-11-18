//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The router is the only process with the project bearer token. Task bridges
// listen on root-only Unix sockets, never on the task network.
func desktopSocket(name string) string {
	return filepath.Join("/run/ov-desktops", name, "control.sock")
}
func desktopTransport(name string) *http.Transport {
	return &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", desktopSocket(name))
	}}
}
func desktopUID(name string) uint32 {
	sum := sha256.Sum256([]byte(name))
	return 20000 + (binary.BigEndian.Uint32(sum[:4])%500000000)*2
}
func taskEnvironment(name string) []string {
	uid := desktopUID(name)
	home := filepath.Join("/home/ubuntu/.desktops", name, "home")
	return []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + home,
		"OPENVIBELY_DESKTOP_ID=" + name, "OPENVIBELY_DESKTOP_PORT=10000", "OPENVIBELY_ISOLATED=1",
		"OPENVIBELY_DESKTOP_UID=" + strconv.FormatUint(uint64(uid), 10),
		"TZ=" + os.Getenv("TZ"), "DISPLAY=:100"}
}
func shellCommand(ctx context.Context, command, directory string) (*exec.Cmd, error) {
	if os.Getenv("OPENVIBELY_ISOLATED") != "1" {
		// Unit tests run without a desktop; production startup never takes this path.
		if os.Getenv("OPENVIBELY_DESKTOP_ID") != "" {
			return nil, fmt.Errorf("desktop shell isolation unavailable")
		}
		return exec.CommandContext(ctx, "/bin/bash", "-lc", command), nil
	}
	uid, err := strconv.ParseUint(os.Getenv("OPENVIBELY_DESKTOP_UID"), 10, 32)
	if err != nil || uid < 20000 {
		return nil, fmt.Errorf("invalid shell identity")
	}
	// setpriv drops all capabilities and disallows setuid executables. Neither
	// Xauthority nor the bridge token is inherited by shell jobs.
	cmd := exec.CommandContext(ctx, "/usr/bin/setpriv", "--reuid="+strconv.FormatUint(uid, 10), "--regid="+strconv.FormatUint(uid+1, 10), "--clear-groups", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all", "--no-new-privs", "/bin/bash", "-c", command)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + computerHome(), "USER=ov-desktop", "LOGNAME=ov-desktop", "LANG=C.UTF-8", "TZ=" + os.Getenv("TZ"), "TMPDIR=/tmp/agent"}
	return cmd, nil
}

// startIsolatedDesktop keeps networking in a private namespace. slirp supplies
// outbound networking without exposing the parent namespace's loopback services.
func startIsolatedDesktop(name string) (*desktopProcess, error) {
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("desktop isolation requires the trusted supervisor")
	}
	if err := validateDesktopStorage(name); err != nil {
		return nil, err
	}
	dir := filepath.Dir(desktopSocket(name))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	for _, f := range []string{"namespace-ready", "network-ready", "control.sock"} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	p := &desktopProcess{done: make(chan struct{})}
	p.cmd = exec.Command("unshare", "--mount", "--net", "--pid", "--ipc", "--fork", "--mount-proc", "--kill-child=SIGKILL", "/usr/local/bin/start-isolated-desktop")
	p.cmd.Env = taskEnvironment(name)
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	p.cmd.Stderr = os.Stderr
	if err := p.cmd.Start(); err != nil {
		return nil, err
	}
	go func() { p.cmd.Wait(); syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); close(p.done) }()
	failed := true
	defer func() {
		if failed {
			syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		}
	}()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "namespace-ready")); err == nil {
			break
		}
		select {
		case <-p.done:
			return nil, fmt.Errorf("desktop namespace setup failed")
		case <-deadline.C:
			return nil, fmt.Errorf("desktop namespace setup timed out")
		case <-ticker.C:
		}
	}
	children, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", p.cmd.Process.Pid, p.cmd.Process.Pid))
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(children))
	if len(fields) != 1 {
		return nil, fmt.Errorf("invalid namespace init")
	}
	p.initPID, err = strconv.Atoi(fields[0])
	if err != nil {
		return nil, err
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer readyR.Close()
	network := exec.Command("slirp4netns", "--configure", "--disable-host-loopback", "--mtu=65520", "--ready-fd=3", strconv.Itoa(p.cmd.Process.Pid), "tap0")
	network.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	network.ExtraFiles = []*os.File{readyW}
	network.Stderr = os.Stderr
	if err = network.Start(); err != nil {
		readyW.Close()
		return nil, err
	}
	readyW.Close()
	go func() { network.Wait(); syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL) }()
	go func() { <-p.done; network.Process.Kill() }()
	readyR.SetReadDeadline(time.Now().Add(15 * time.Second))
	var b [1]byte
	if _, err = readyR.Read(b[:]); err != nil {
		return nil, fmt.Errorf("desktop networking failed: %w", err)
	}
	if err = os.WriteFile(filepath.Join(dir, "network-ready"), nil, 0600); err != nil {
		return nil, err
	}
	failed = false
	return p, nil
}

func taskOwnFile(f *os.File) error {
	if os.Getenv("OPENVIBELY_ISOLATED") != "1" {
		return nil
	}
	uid, err := strconv.Atoi(os.Getenv("OPENVIBELY_DESKTOP_UID"))
	if err != nil {
		return err
	}
	if err = f.Chown(uid, uid); err != nil {
		return err
	}
	return f.Chmod(0660)
}
func taskMkdirAll(root *os.Root, path string) error {
	if err := root.MkdirAll(path, 0700); err != nil {
		return err
	}
	if os.Getenv("OPENVIBELY_ISOLATED") != "1" {
		return nil
	}
	uid, err := strconv.Atoi(os.Getenv("OPENVIBELY_DESKTOP_UID"))
	if err != nil {
		return err
	}
	for path != "." {
		if err = root.Chown(path, uid, uid); err != nil {
			return err
		}
		if err = root.Chmod(path, 0770); err != nil {
			return err
		}
		path = filepath.Dir(path)
	}
	return nil
}
func taskWriteFile(root *os.Root, path string, data []byte) error {
	f, err := openWorkspaceFile(root, path, os.O_CREATE|os.O_WRONLY, false)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = taskOwnFile(f); err != nil {
		return err
	}
	if err = f.Truncate(0); err != nil {
		return err
	}
	_, err = f.Write(data)
	return err
}

var desktopIdentityMu sync.Mutex

// Registry persists the UID binding, preventing hash collisions from giving a
// later task access to an older task's retained files.
func registerDesktopIdentity(name string) error {
	desktopIdentityMu.Lock()
	defer desktopIdentityMu.Unlock()
	path := "/home/ubuntu/.ov-desktop-identities.json"
	if info, err := os.Lstat(path); err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("unsafe identity registry")
		}
	}
	ids := map[string]uint32{}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &ids); err != nil {
			return err
		}
	}
	uid := desktopUID(name)
	for other, id := range ids {
		if other != name && id == uid {
			return fmt.Errorf("desktop identity collision")
		}
	}
	if old, ok := ids[name]; ok {
		if old != uid {
			return fmt.Errorf("desktop identity changed")
		}
		return nil
	}
	ids[name] = uid
	data, err = json.Marshal(ids)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".ov-identities-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func validateDesktopStorage(name string) error {
	for _, path := range []string{"/home/ubuntu", "/home/ubuntu/.desktops", filepath.Join("/home/ubuntu/.desktops", name)} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe desktop storage")
		}
	}
	// Task identities must be scoped names, never arbitrary filesystem paths.
	if !strings.HasPrefix(name, "task-") {
		return fmt.Errorf("invalid desktop identity")
	}
	return registerDesktopIdentity(name)
}

func stopDesktopProcess(p *desktopProcess) {
	select {
	case <-p.done:
		return
	default:
	}
	if p.initPID > 0 {
		syscall.Kill(p.initPID, syscall.SIGTERM)
	} else {
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// The shell uses the desktop UID and a dedicated primary GID for job tracking.
// Match that GID even when children escape their process group with setsid.
// Each bridge sees only its task's PID namespace.
func stopShellProcesses(ctx context.Context) error {
	if os.Getenv("OPENVIBELY_ISOLATED") != "1" {
		return nil
	}
	uid, err := strconv.ParseUint(os.Getenv("OPENVIBELY_DESKTOP_UID"), 10, 32)
	if err != nil || uid < 20000 {
		return fmt.Errorf("invalid shell identity")
	}
	for {
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return err
		}
		live := false
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid <= 1 {
				continue
			}
			info, err := os.Stat("/proc/" + entry.Name())
			if err != nil {
				continue
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != uint32(uid) || stat.Gid != uint32(uid+1) {
				continue
			}
			data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
			if err != nil {
				continue
			}
			// Zombies cannot observe input and are reaped by the namespace init.
			at := strings.LastIndex(string(data), ") ")
			if at >= 0 && strings.HasPrefix(string(data)[at+2:], "Z ") {
				continue
			}
			live = true
			syscall.Kill(pid, syscall.SIGKILL)
		}
		if !live {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// Tools that touch X11 or invoke browser/image libraries do not run as root:
// those libraries may consult configuration in the desktop's writable home.
func desktopCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	if os.Getenv("OPENVIBELY_ISOLATED") != "1" {
		return exec.CommandContext(ctx, name, args...)
	}
	uid, err := strconv.ParseUint(os.Getenv("OPENVIBELY_DESKTOP_UID"), 10, 32)
	if err != nil || uid < 20000 {
		return exec.CommandContext(ctx, "/bin/false")
	}
	identity := strconv.FormatUint(uid, 10)
	prefix := []string{"--reuid=" + identity, "--regid=" + identity, "--clear-groups", "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all", "--no-new-privs", name}
	cmd := exec.CommandContext(ctx, "/usr/bin/setpriv", append(prefix, args...)...)
	base := filepath.Join("/home/ubuntu/.desktops", os.Getenv("OPENVIBELY_DESKTOP_ID"))
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + computerHome(), "DISPLAY=:100", "XAUTHORITY=/tmp/ov-display-authority", "XDG_CONFIG_HOME=" + base + "/config", "XDG_CACHE_HOME=" + base + "/cache", "TZ=" + os.Getenv("TZ"), "LANG=C.UTF-8"}
	return cmd
}

func validateIsolationHost() error {
	if info, err := os.Stat("/dev/net/tun"); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("project computer requires /dev/net/tun")
	}
	// Check mount/network/PID isolation before advertising a ready supervisor.
	// No request or user-controlled data is passed to this privileged probe.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "unshare", "--mount", "--net", "--pid", "--fork", "--mount-proc", "/bin/sh", "-c", "ip link set lo up && mount -o remount,hidepid=2 /proc").Run(); err != nil {
		return fmt.Errorf("project computer requires namespace support and SYS_ADMIN/NET_ADMIN: %w", err)
	}
	return nil
}

// Detached command descendants still occupy a desktop even after their job's
// original process exits. Inspect the task PID namespace, not other desktops.
func shellProcessesRunning() bool {
	if os.Getenv("OPENVIBELY_ISOLATED") != "1" {
		return false
	}
	uid, err := strconv.ParseUint(os.Getenv("OPENVIBELY_DESKTOP_UID"), 10, 32)
	if err != nil || uid < 20000 {
		return true
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		info, err := os.Stat("/proc/" + entry.Name())
		if err != nil {
			continue
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid == uint32(uid) && stat.Gid == uint32(uid+1) {
			return true
		}
	}
	return false
}
