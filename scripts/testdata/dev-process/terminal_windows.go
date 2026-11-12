// SPDX-License-Identifier: MPL-2.0
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type terminalBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *terminalBuffer) Write(data []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(data)
}
func (b *terminalBuffer) snapshot() string {
	b.Lock()
	defer b.Unlock()
	return b.String()
}

// Exercise real console handles even when the surrounding test runner uses
// pipes. Nothing is displayed in a new console window. A fixture-owned job
// prevents its terminal clients escaping if the test driver is interrupted.
func terminal(args []string) error {
	var inRead, inWrite, outRead, outWrite windows.Handle
	if err := windows.CreatePipe(&inRead, &inWrite, nil, 0); err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(inRead) }()
	input := os.NewFile(uintptr(inWrite), "terminal-input")
	defer func() { _ = input.Close() }()
	if err := windows.CreatePipe(&outRead, &outWrite, nil, 0); err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(outWrite) }()
	output := os.NewFile(uintptr(outRead), "terminal-output")
	defer func() { _ = output.Close() }()
	var console windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: 100, Y: 25}, inRead, outWrite, 0, &console); err != nil {
		return err
	}
	consoleClosed := false
	defer func() {
		if !consoleClosed {
			windows.ClosePseudoConsole(console)
		}
	}()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(job) }()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return err
	}
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return err
	}
	defer attributes.Delete()
	// PSEUDOCONSOLE takes the opaque handle value, not a pointer to that value.
	update := windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")
	if ok, _, err := update.Call(uintptr(unsafe.Pointer(attributes.List())), 0, windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(console), unsafe.Sizeof(console), 0, 0); ok == 0 {
		return err
	}
	if err := attributes.Update(0x0002000d, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		return err
	}
	startup := windows.StartupInfoEx{}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	// Explicitly request console defaults rather than inheriting the outer
	// test runner's redirected standard handles into this terminal client.
	startup.Flags = windows.STARTF_USESHOWWINDOW | windows.STARTF_USESTDHANDLES
	startup.ShowWindow = windows.SW_HIDE
	startup.ProcThreadAttributeList = attributes.List()
	application, err := windows.UTF16PtrFromString(args[0])
	if err != nil {
		return err
	}
	line, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
	if err != nil {
		return err
	}
	var process windows.ProcessInformation
	if err := windows.CreateProcess(application, line, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT, nil, nil, &startup.StartupInfo, &process); err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(process.Process) }()
	defer func() { _ = windows.CloseHandle(process.Thread) }()
	_ = windows.CloseHandle(inRead)
	inRead = 0
	_ = windows.CloseHandle(outWrite)
	outWrite = 0
	var captured terminalBuffer
	drained := make(chan struct{})
	go func() { _, _ = io.Copy(&captured, output); close(drained) }()
	sent := false
	deadline := time.Now().Add(20 * time.Second)
	var failure error
	for {
		if !sent && strings.Contains(captured.snapshot(), "TERMINAL-READY") {
			text := "terminal input λ\r\n"
			if os.Getenv("DEV_TEST_TERMINAL_SIGNAL") == "1" {
				text = "\x03"
			}
			if _, err := input.WriteString(text); err != nil {
				failure = err
				break
			}
			sent = true
		}
		state, err := windows.WaitForSingleObject(process.Process, 10)
		if err != nil {
			failure = err
			break
		}
		if state == windows.WAIT_OBJECT_0 {
			break
		}
		if time.Now().After(deadline) {
			failure = errors.New("terminal command did not exit before deadline")
			break
		}
	}
	if failure != nil {
		_ = windows.TerminateJobObject(job, 1)
	}
	_, _ = windows.WaitForSingleObject(process.Process, 5000)
	windows.ClosePseudoConsole(console)
	consoleClosed = true
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		failure = errors.Join(failure, errors.New("terminal output did not drain"))
	}
	fmt.Print(captured.snapshot())
	var code uint32
	if err := windows.GetExitCodeProcess(process.Process, &code); err != nil {
		return err
	}
	if code != 0 {
		failure = errors.Join(failure, fmt.Errorf("terminal command exited %d", code))
	}
	return failure
}
