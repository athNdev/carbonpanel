// SPDX-License-Identifier: MPL-2.0
// carbon-dev-supervisor gives one Windows development command durable process
// ownership. It is a developer tool, not part of the deployed controller.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: carbon-dev-supervisor COMMAND [ARG...]")
		os.Exit(1)
	}
	code, err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "[dev supervisor]", err)
		os.Exit(1)
	}
	os.Exit(code)
}
