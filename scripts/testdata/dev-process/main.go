// SPDX-License-Identifier: MPL-2.0
// Native fixture: unlike JavaScript runtimes, exiting this parent does not
// implicitly reap the descendant. The development supervisor must own it.
package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"time"
)

func main() {
	mode, ready := os.Args[1], os.Args[2]
	if mode == "terminal" {
		if err := terminal(os.Args[2:]); err != nil {
			panic(err)
		}
		return
	}
	if mode == "build" {
		// Pretend to be the initial Go compiler in the bootstrap-stop test.
		mode, ready = "parent", os.Getenv("DEV_TEST_BUILD_READY")
	}
	if mode == "stdin" {
		_, _ = io.Copy(os.Stdout, os.Stdin)
		return
	}
	if mode == "child" {
		_ = os.WriteFile(ready, []byte("ready"), 0o600)
		for {
			if _, err := os.Stat(ready + ".stop"); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	child := exec.Command(os.Args[0], "child", ready+".child")
	hide(child)
	if err := child.Start(); err != nil {
		panic(err)
	}
	workingDirectory, _ := os.Getwd()
	data, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "ppid": os.Getppid(), "grandchild": child.Process.Pid, "cwd": workingDirectory, "args": os.Args[3:], "marker": os.Getenv("DEV_TEST_MARKER")})
	if err := os.WriteFile(ready+".tmp", data, 0o600); err != nil {
		panic(err)
	}
	if err := os.Rename(ready+".tmp", ready); err != nil {
		panic(err)
	}
	for {
		if _, err := os.Stat(ready + ".exit"); err == nil {
			if mode == "panic" {
				panic("fixture root crashed")
			}
			os.Exit(17)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
