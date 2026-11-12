// SPDX-License-Identifier: MPL-2.0
//go:build !windows

package main

import "errors"

func run([]string) (int, error) {
	return 1, errors.New("Windows supervisor invoked on another operating system; use the canonical development launcher")
}
