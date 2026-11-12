// SPDX-License-Identifier: MPL-2.0
package main

import (
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A root may never reach its ready file if the helper dies during startup.
// Assert containment using its actual process HANDLE before any thread runs.
func TestSuspendedRootBelongsToJobBeforeResume(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if job != 0 {
			_ = windows.CloseHandle(job)
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		t.Fatal(err)
	}
	child, err := startSuspended([]string{executable, "-test.run=^$"}, job)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(child.Process) }()
	defer func() { _ = windows.CloseHandle(child.Thread) }()
	defer func() {
		// Failures also clean up only this exact held process object.
		_ = windows.TerminateProcess(child.Process, 1)
		_, _ = windows.WaitForSingleObject(child.Process, 5000)
	}()
	if state, err := windows.WaitForSingleObject(child.Process, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("suspended root exited before job closure: state=%d err=%v", state, err)
	}
	if err := windows.CloseHandle(job); err != nil {
		t.Fatal(err)
	}
	job = 0
	if state, err := windows.WaitForSingleObject(child.Process, 5000); err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("closing the job left its never-resumed root alive: state=%d err=%v", state, err)
	}
}
