// SPDX-License-Identifier: MPL-2.0
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PROC_THREAD_ATTRIBUTE_JOB_LIST from the Windows SDK (Windows 10+).
const processAttributeJobList = 0x0002000d

var ownerInterruptHandler = windows.NewCallback(func(event uint32) uintptr {
	if event == windows.CTRL_C_EVENT || event == windows.CTRL_BREAK_EVENT {
		return 1 // The JavaScript owner stops the jobs as one session.
	}
	return 0
})

// A job handle belongs only to this supervisor. Children inherit stdio, not the
// handle that keeps their job alive. Closing it kills even orphaned descendants.
func run(args []string) (int, error) {
	parent, err := openParent()
	if err != nil {
		return 1, err
	}
	defer func() { _ = windows.CloseHandle(parent) }()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 1, fmt.Errorf("create process job: %w", err)
	}
	defer func() { _ = windows.CloseHandle(job) }()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return 1, fmt.Errorf("set process job limits: %w", err)
	}
	process, err := startSuspended(args, job)
	if err != nil {
		return 1, err
	}
	defer func() { _ = windows.CloseHandle(process.Process) }()
	defer func() { _ = windows.CloseHandle(process.Thread) }()
	if state, err := windows.WaitForSingleObject(parent, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		return 1, errors.New("development launcher exited before command startup")
	}
	if _, err = windows.ResumeThread(process.Thread); err != nil {
		return 1, fmt.Errorf("resume contained command: %w", err)
	}
	state, err := windows.WaitForMultipleObjects([]windows.Handle{process.Process, parent}, false, windows.INFINITE)
	if err != nil {
		return 1, fmt.Errorf("wait for command or launcher: %w", err)
	}
	code := uint32(1)
	if state == windows.WAIT_OBJECT_0 {
		if err := windows.GetExitCodeProcess(process.Process, &code); err != nil {
			return 1, fmt.Errorf("read command exit status: %w", err)
		}
	}
	if err := terminateJob(job); err != nil {
		return 1, err
	}
	return int(code), nil
}

// Keep a HANDLE to the actual creating process, not a PID that may be reused.
// A vanished parent must not be replaced by a newer process with that old PID.
func openParent() (windows.Handle, error) {
	pid := windows.Getppid()
	if pid <= 0 {
		return 0, errors.New("cannot identify development launcher")
	}
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, fmt.Errorf("open development launcher: %w", err)
	}
	var parentCreated, selfCreated, exited, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(parent, &parentCreated, &exited, &kernel, &user); err == nil {
		err = windows.GetProcessTimes(windows.CurrentProcess(), &selfCreated, &exited, &kernel, &user)
	}
	if err != nil || parentCreated.Nanoseconds() > selfCreated.Nanoseconds() {
		_ = windows.CloseHandle(parent)
		return 0, errors.New("development launcher identity is unavailable or has been reused")
	}
	return parent, nil
}

func startSuspended(args []string, job windows.Handle) (windows.ProcessInformation, error) {
	var process windows.ProcessInformation
	executable, err := exec.LookPath(args[0])
	if err != nil {
		return process, fmt.Errorf("find command: %w", err)
	}
	application, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return process, err
	}
	commandLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{executable}, args[1:]...)))
	if err != nil {
		return process, err
	}
	// The JavaScript runtime can hide a helper by detaching its console even
	// while passing console handles. Rejoin the existing parent console; this
	// allocates no window and is harmless when a console is already attached.
	attachConsole := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
	_, _, _ = attachConsole.Call(^uintptr(0) & 0xffffffff)
	stdio := []windows.Handle{windows.Handle(os.Stdin.Fd()), windows.Handle(os.Stdout.Fd()), windows.Handle(os.Stderr.Fd())}
	inherited := make([]windows.Handle, len(stdio))
	for i, handle := range stdio {
		if err := windows.DuplicateHandle(windows.CurrentProcess(), handle, windows.CurrentProcess(), &inherited[i], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			for _, opened := range inherited {
				if opened != 0 {
					_ = windows.CloseHandle(opened)
				}
			}
			return process, fmt.Errorf("inherit command stdio: %w", err)
		}
	}
	defer func() {
		for _, handle := range inherited {
			_ = windows.CloseHandle(handle)
		}
	}()
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return process, err
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&inherited[0]), uintptr(len(inherited))*unsafe.Sizeof(inherited[0])); err != nil {
		return process, err
	}
	// Kernel-side job assignment closes the create-then-assign race: killing
	// this helper during startup cannot strand an unassigned suspended root.
	jobs := []windows.Handle{job}
	if err := attributes.Update(processAttributeJobList, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(jobs[0])); err != nil {
		return process, fmt.Errorf("set atomic process job: %w", err)
	}
	startup := windows.StartupInfoEx{}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	startup.ShowWindow = windows.SW_HIDE
	startup.StdInput, startup.StdOutput, startup.StdErr = inherited[0], inherited[1], inherited[2]
	startup.ProcThreadAttributeList = attributes.List()
	flags := uint32(windows.CREATE_SUSPENDED | windows.EXTENDED_STARTUPINFO_PRESENT)
	console := false
	for _, handle := range stdio {
		var mode uint32
		if windows.GetConsoleMode(handle, &mode) == nil {
			console = true
		}
	}
	if !console {
		flags |= windows.CREATE_NO_WINDOW
	} else {
		// AttachConsole resets Go's earlier console handler registration.
		// Keep ownership here until the JavaScript owner handles interruption;
		// don't race it by exiting this keeper on the same console event.
		setHandler := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCtrlHandler")
		if ok, _, err := setHandler.Call(ownerInterruptHandler, 1); ok == 0 {
			return process, fmt.Errorf("retain console process ownership: %w", err)
		}
		flags |= windows.CREATE_NEW_PROCESS_GROUP
	}
	if err := windows.CreateProcess(application, commandLine, nil, nil, true, flags, nil, nil, &startup.StartupInfo, &process); err != nil {
		return process, fmt.Errorf("start contained command: %w", err)
	}
	return process, nil
}

func terminateJob(job windows.Handle) error {
	if err := windows.TerminateJobObject(job, 1); err != nil {
		return fmt.Errorf("terminate owned process job: %w", err)
	}
	// The termination request is asynchronous. Wait for all job members, not
	// just its root, before reporting that this command's session has ended.
	var accounting struct {
		TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
		TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
			return fmt.Errorf("check owned process job: %w", err)
		}
		if accounting.ActiveProcesses == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("owned process job did not terminate before deadline")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
