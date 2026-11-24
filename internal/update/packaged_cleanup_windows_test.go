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

func terminateWindowsTestExecutable(executables ...string) error {
	want := make([]string, 0, len(executables))
	for _, executable := range executables {
		want = append(want, windowsCleanupPath(executable))
	}
	// Scan both paths until a fresh snapshot has no matches. A process
	// disappearing during inspection also requires another snapshot.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rescan, err := terminateWindowsTestExecutableSnapshot(want, deadline)
		if err != nil {
			return err
		}
		if !rescan {
			return nil
		}
	}
	return fmt.Errorf("executable cleanup did not settle before its deadline")
}

func terminateWindowsTestExecutableSnapshot(want []string, deadline time.Time) (bool, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false, fmt.Errorf("snapshot processes: %w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	var failures []error
	rescan := false
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		// Avoid opening unrelated system processes. Full paths below prevent
		// killing another test's executable with the same basename.
		candidate := false
		for _, path := range want {
			if strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), filepath.Base(path)) {
				candidate = true
				break
			}
		}
		if !candidate {
			continue
		}
		if !time.Now().Before(deadline) {
			return rescan, fmt.Errorf("process scan exceeded cleanup deadline")
		}
		inspectAgain, stopErr := terminateWindowsTestProcess(entry.ProcessID, want, deadline)
		rescan = rescan || inspectAgain
		if stopErr != nil {
			failures = append(failures, fmt.Errorf("PID %d: %w", entry.ProcessID, stopErr))
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		failures = append(failures, fmt.Errorf("enumerate processes: %w", err))
	}
	return rescan, errors.Join(failures...)
}

func terminateWindowsTestProcess(pid uint32, want []string, deadline time.Time) (bool, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return true, nil // Disappeared since the snapshot; a successor may now exist.
	}
	if err != nil {
		return false, fmt.Errorf("open process: %w", err)
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		if status, _ := windows.WaitForSingleObject(handle, 0); status == windows.WAIT_OBJECT_0 {
			return true, nil
		}
		return false, fmt.Errorf("read executable path: %w", err)
	}
	imagePath := windowsCleanupPath(windows.UTF16ToString(buffer[:size]))
	matched := false
	for _, path := range want {
		if strings.EqualFold(imagePath, path) {
			matched = true
			break
		}
	}
	if !matched {
		return false, nil
	}
	// Request termination access only after identifying our executable. Duplicate
	// the inspected handle rather than reopening the PID: it refers to the same
	// process object even if that process has exited in the meantime.
	var termination windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), handle, windows.CurrentProcess(), &termination, windows.PROCESS_TERMINATE, false, 0); err != nil {
		if status, _ := windows.WaitForSingleObject(handle, 0); status == windows.WAIT_OBJECT_0 {
			return true, nil
		}
		return false, fmt.Errorf("request termination access: %w", err)
	}
	defer windows.CloseHandle(termination)
	if err := windows.TerminateProcess(termination, 1); err != nil {
		if status, _ := windows.WaitForSingleObject(handle, 0); status == windows.WAIT_OBJECT_0 {
			return true, nil
		}
		return false, fmt.Errorf("terminate: %w", err)
	}
	remaining := min(3*time.Second, time.Until(deadline))
	if remaining <= 0 {
		return true, fmt.Errorf("cleanup deadline reached while waiting for exit")
	}
	status, err := windows.WaitForSingleObject(handle, uint32(remaining.Milliseconds()))
	if err != nil {
		return false, fmt.Errorf("wait for exit: %w", err)
	}
	if status != windows.WAIT_OBJECT_0 {
		return false, fmt.Errorf("process did not exit before wait deadline (wait status %#x)", status)
	}
	return true, nil
}
