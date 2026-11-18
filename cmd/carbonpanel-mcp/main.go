// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Command carbonpanel-mcp exposes Carbon Panel operator workflows as an MCP
// server over stdio. It requires CARBONPANEL_URL and an API token in
// CARBONPANEL_API_TOKEN (or CARBONPANEL_TOKEN). All diagnostics go to stderr;
// stdout is reserved for MCP JSON-RPC frames.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/athNdev/carbon-panel/internal/mcp"
)

// Build metadata. The distribution lane injects these with linker flags:
//
//	-X main.version=<mcp artifact version>
//	-X main.commit=<full source commit>
//	-X main.buildDate=<RFC3339 build time>
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and build metadata")
	flag.Parse()
	if *showVersion {
		fmt.Printf("carbonpanel-mcp %s commit=%s built=%s\n", version, commit, buildDate)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := mcp.Run(ctx); err != nil {
		log.Printf("carbonpanel-mcp: %v", err)
		os.Exit(1)
	}
}
