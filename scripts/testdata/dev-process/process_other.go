// SPDX-License-Identifier: MPL-2.0
//go:build !windows

package main

import (
	"errors"
	"os/exec"
)

func hide(*exec.Cmd) {}

func terminal([]string) error { return errors.New("ConPTY fixture requires Windows") }
