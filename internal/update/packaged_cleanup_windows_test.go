//go:build windows

package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Register immediately after TempDir so this runs after process/log cleanup,
// but before testing removes the directory and loses the offending path.
func registerPackagedCleanupDiagnostics(t *testing.T, root string) {
	t.Cleanup(func() {
		started := time.Now()
		defer func() { t.Logf("phase remove scenario directory: %s", time.Since(started)) }()
		deadline := started.Add(2 * time.Second)
		for {
			err := os.RemoveAll(root)
			if err == nil {
				return
			}
			if time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			var pathErr *os.PathError
			if errors.As(err, &pathErr) {
				logWindowsPathUsers(t, pathErr.Path)
			}
			t.Errorf("remove scenario directory: %v", err)
			return
		}
	})
}

// This is diagnostic only: a process holding a path is not necessarily owned
// by this test, so never terminate processes discovered here.
func logWindowsPathUsers(t *testing.T, path string) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Logf("inspect locked path %q: %v", path, err)
		return
	}
	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Logf("open locked path %q for inspection: %v", path, err)
		return
	}
	defer windows.CloseHandle(h)
	// FILE_PROCESS_IDS_USING_FILE_INFORMATION: ULONG count followed by an
	// aligned array of ULONG_PTR process IDs. Works on amd64 and arm64.
	var users struct {
		count uint32
		pids  [4096]uintptr
	}
	var status windows.IO_STATUS_BLOCK
	const fileProcessIdsUsingFileInformation = 47
	err = windows.NtQueryInformationFile(h, &status, (*byte)(unsafe.Pointer(&users)), uint32(unsafe.Sizeof(users)), fileProcessIdsUsingFileInformation)
	if err != nil {
		t.Logf("query processes using %q: %v", path, err)
		return
	}
	t.Logf("path %q has %d process handles (includes this diagnostic process, pid=%d)", path, users.count, os.Getpid())
	for _, pid := range users.pids[:min(int(users.count), len(users.pids))] {
		process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			t.Logf("path %q: pid=%d, inspection error=%v", path, pid, err)
			continue
		}
		name := make([]uint16, 32768)
		size := uint32(len(name))
		err = windows.QueryFullProcessImageName(process, 0, &name[0], &size)
		windows.CloseHandle(process)
		t.Logf("path %q: pid=%d executable=%q inspection error=%v", path, pid, windows.UTF16ToString(name[:size]), err)
	}
}

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

// The test owns the job, so it survives app exits and update restarts. Starting
// suspended prevents children from escaping before job assignment.
func startPackagedApp(cmd *exec.Cmd) (func() error, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	closeJob := func() error { return windows.CloseHandle(job) }
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = closeJob()
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err = cmd.Start(); err != nil {
		_ = closeJob()
		return nil, err
	}
	if err = assignAndResumePackagedApp(job, uint32(cmd.Process.Pid)); err != nil {
		_ = cmd.Process.Kill()
		_ = closeJob()
		_ = cmd.Wait()
		return nil, err
	}
	return func() error {
		// Keep the job open while discovering and waiting for members. Closing
		// it first would kill parents before their children can be captured.
		err := drainPackagedAppJob(job, time.Now().Add(5*time.Second))
		return errors.Join(err, closeJob())
	}, nil
}

var cleanupIsProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func drainPackagedAppJob(job windows.Handle, deadline time.Time) error {
	owned := make(map[uint32]*windowsCleanupProcess)
	defer func() {
		for _, process := range owned {
			windows.CloseHandle(process.handle)
		}
	}()
	for time.Now().Before(deadline) {
		// Job membership includes descendants even after their parent exits.
		// Retaining handles prevents PID reuse while draining successive scans.
		var members struct {
			assigned uint32
			count    uint32
			pids     [4096]uintptr
		}
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList,
			uintptr(unsafe.Pointer(&members)), uint32(unsafe.Sizeof(members)), nil); err != nil {
			return fmt.Errorf("query app job members: %w", err)
		}
		if members.count < members.assigned || members.count > uint32(len(members.pids)) {
			return fmt.Errorf("app job member list incomplete: %d of %d", members.count, members.assigned)
		}
		for _, id := range members.pids[:members.count] {
			pid := uint32(id)
			if owned[pid] != nil {
				continue
			}
			process, err := openWindowsCleanupProcess(pid, "app job member")
			if err != nil {
				return fmt.Errorf("inspect app job PID %d: %w", pid, err)
			}
			if process == nil {
				continue // Exited during inspection; rescan before declaring success.
			}
			// A PID could have been reused between the query and OpenProcess.
			var belongs int32
			ok, _, callErr := cleanupIsProcessInJob.Call(uintptr(process.handle), uintptr(job), uintptr(unsafe.Pointer(&belongs)))
			if ok == 0 || belongs == 0 {
				windows.CloseHandle(process.handle)
				if ok == 0 {
					return fmt.Errorf("check app job membership for PID %d: %w", pid, callErr)
				}
				continue
			}
			owned[pid] = process
		}
		// Capture all members before requesting termination. Kill captured
		// processes rather than the job so newly spawned children are still
		// discoverable on the next scan, before we terminate and wait for them.
		for _, process := range owned {
			if err := stopWindowsCleanupProcess(process); err != nil {
				return fmt.Errorf("stop app job PID %d: %w", process.pid, err)
			}
		}
		for _, process := range owned {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return fmt.Errorf("app job cleanup deadline reached waiting for PID %d", process.pid)
			}
			status, err := windows.WaitForSingleObject(process.handle, uint32(remaining.Milliseconds()))
			if err != nil || status != windows.WAIT_OBJECT_0 {
				return fmt.Errorf("wait for app job PID %d: status=%#x error=%v", process.pid, status, err)
			}
		}
		if members.count == 0 {
			return nil
		}
	}
	return fmt.Errorf("app job cleanup did not finish before its deadline")
}

func assignAndResumePackagedApp(job windows.Handle, pid uint32) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	if err = windows.AssignProcessToJobObject(job, process); err != nil {
		return err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		return err
	}
	return fmt.Errorf("find suspended app thread: %w", err)
}
