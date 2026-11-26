//go:build windows

package update

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func killExecutablePlatform(t *testing.T, executables ...string) bool {
	t.Helper()
	started := time.Now()
	if err := terminateWindowsTestExecutable(executables...); err != nil {
		t.Errorf("cleanup executables %v after %s: %v", executables, time.Since(started), err)
	}
	return true
}

// Resolve short Windows paths (RUNNER~1 in CI) before comparing image paths.
// Missing files are allowed: an update may already have removed its helper.
func windowsCleanupPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

// Retain handles across snapshots, including exited parents, so a child created
// during termination can still be attributed to the test-owned process tree.
type windowsCleanupProcess struct {
	handle  windows.Handle
	pid     uint32
	created uint64
	name    string
}

func terminateWindowsTestExecutable(executables ...string) error {
	want := make([]string, 0, len(executables))
	for _, executable := range executables {
		want = append(want, windowsCleanupPath(executable))
	}
	owned := make(map[uint32]*windowsCleanupProcess)
	defer func() {
		for _, process := range owned {
			_ = windows.CloseHandle(process.handle)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rescan, err := collectWindowsCleanupProcesses(want, owned, deadline)
		if err != nil {
			return err
		}
		// Discovery finishes before any process is killed. Request termination
		// for the whole captured tree before waiting on any individual process.
		var failures []error
		var stopping []*windowsCleanupProcess
		for _, process := range owned {
			status, err := windows.WaitForSingleObject(process.handle, 0)
			if err == nil && status == windows.WAIT_OBJECT_0 {
				continue
			}
			rescan = true
			if err := stopWindowsCleanupProcess(process); err != nil {
				failures = append(failures, fmt.Errorf("%s PID %d: %w", process.name, process.pid, err))
				continue
			}
			stopping = append(stopping, process)
		}
		for _, process := range stopping {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				failures = append(failures, fmt.Errorf("%s PID %d: cleanup deadline reached", process.name, process.pid))
				continue
			}
			status, err := windows.WaitForSingleObject(process.handle, uint32(remaining.Milliseconds()))
			if err != nil || status != windows.WAIT_OBJECT_0 {
				failures = append(failures, fmt.Errorf("%s PID %d: wait for exit status %#x: %v", process.name, process.pid, status, err))
			}
		}
		if err := errors.Join(failures...); err != nil {
			return err
		}
		if !rescan {
			return nil
		}
	}
	return fmt.Errorf("process tree cleanup did not settle before its deadline")
}

func collectWindowsCleanupProcesses(want []string, owned map[uint32]*windowsCleanupProcess, deadline time.Time) (bool, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, fmt.Errorf("snapshot processes: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	var entries []windows.ProcessEntry32
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		entries = append(entries, entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return false, fmt.Errorf("enumerate processes: %w", err)
	}
	rescan := false
	// Revisit the same snapshot until every descendant of a discovered root
	// has been captured, regardless of the process enumeration order.
	seen := make(map[uint32]bool)
	for {
		added := false
		for _, entry := range entries {
			if !time.Now().Before(deadline) {
				return rescan, fmt.Errorf("process discovery exceeded cleanup deadline")
			}
			if owned[entry.ProcessID] != nil || seen[entry.ProcessID] {
				continue
			}
			parent := owned[entry.ParentProcessID]
			name := windows.UTF16ToString(entry.ExeFile[:])
			candidate := parent != nil
			for _, path := range want {
				candidate = candidate || strings.EqualFold(name, filepath.Base(path))
			}
			if !candidate {
				continue
			}
			process, err := openWindowsCleanupProcess(entry.ProcessID, name)
			if err != nil {
				return rescan, fmt.Errorf("%s PID %d: %w", name, entry.ProcessID, err)
			}
			if process == nil {
				seen[entry.ProcessID] = true
				rescan = true // It may have launched a successor before exiting.
				continue
			}
			matched, err := windowsCleanupProcessMatches(process, want)
			if err == nil && !matched && parent != nil {
				matched, err = windowsCleanupChildMatches(parent, process)
			}
			if err != nil {
				windows.CloseHandle(process.handle)
				return rescan, fmt.Errorf("%s PID %d: %w", name, entry.ProcessID, err)
			}
			if !matched {
				if status, _ := windows.WaitForSingleObject(process.handle, 0); status == windows.WAIT_OBJECT_0 {
					seen[entry.ProcessID] = true
					rescan = true
				}
				windows.CloseHandle(process.handle)
				// Its parent might be discovered later in this snapshot.
				continue
			}
			owned[entry.ProcessID] = process
			added, rescan = true, true
		}
		if !added {
			return rescan, nil
		}
	}
}

func openWindowsCleanupProcess(pid uint32, name string) (*windowsCleanupProcess, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open process: %w", err)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("read process creation time: %w", err)
	}
	return &windowsCleanupProcess{handle: handle, pid: pid, created: cleanupFiletime(created), name: name}, nil
}

func windowsCleanupProcessMatches(process *windowsCleanupProcess, want []string) (bool, error) {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process.handle, 0, &buffer[0], &size); err != nil {
		if status, _ := windows.WaitForSingleObject(process.handle, 0); status == windows.WAIT_OBJECT_0 {
			return false, nil
		}
		return false, fmt.Errorf("read executable path: %w", err)
	}
	imagePath := windowsCleanupPath(windows.UTF16ToString(buffer[:size]))
	for _, path := range want {
		if strings.EqualFold(imagePath, path) {
			return true, nil
		}
	}
	return false, nil
}

func cleanupFiletime(value windows.Filetime) uint64 {
	return uint64(value.HighDateTime)<<32 | uint64(value.LowDateTime)
}

func windowsCleanupChildMatches(parent, child *windowsCleanupProcess) (bool, error) {
	// A reused parent PID must not claim an older unrelated process. For a
	// retained exited parent, the child must also predate that parent's exit.
	if child.created < parent.created {
		return false, nil
	}
	status, err := windows.WaitForSingleObject(parent.handle, 0)
	if err != nil {
		return false, err
	}
	if status == windows.WAIT_OBJECT_0 {
		var created, exited, kernel, user windows.Filetime
		if err := windows.GetProcessTimes(parent.handle, &created, &exited, &kernel, &user); err != nil {
			return false, err
		}
		return child.created <= cleanupFiletime(exited), nil
	}
	return true, nil
}

func stopWindowsCleanupProcess(process *windowsCleanupProcess) error {
	// Duplicate the inspected handle instead of reopening a potentially reused
	// PID. Termination access is requested only for confirmed test-owned roots
	// and their descendants.
	var termination windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), process.handle, windows.CurrentProcess(), &termination, windows.PROCESS_TERMINATE, false, 0); err != nil {
		if status, _ := windows.WaitForSingleObject(process.handle, 0); status == windows.WAIT_OBJECT_0 {
			return nil
		}
		return fmt.Errorf("request termination access: %w", err)
	}
	defer windows.CloseHandle(termination)
	if err := windows.TerminateProcess(termination, 1); err != nil {
		if status, _ := windows.WaitForSingleObject(process.handle, 0); status == windows.WAIT_OBJECT_0 {
			return nil
		}
		return fmt.Errorf("terminate: %w", err)
	}
	return nil
}
