// SPDX-License-Identifier: MPL-2.0
package main

import (
	"os/exec"
	"syscall"
)

func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
