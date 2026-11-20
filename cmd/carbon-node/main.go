// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Command carbon-node renders a host-native preflight plan and runs the managed
// node agent after the private daemon and enrollment have been provisioned.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/athNdev/carbon-panel/internal/noderuntime/native"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "run" {
		if err := runAgent(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "managed agent:", err)
			os.Exit(1)
		}
		return
	}
	factsPath := flag.String("facts", "", "path to a bounded HostFacts JSON document (required; no live host is probed)")
	nodeID := flag.String("node-id", "node-local", "stable node ID for the plan")
	unitOut := flag.String("print-unit", "", "also print the generated systemd unit to this path (must be inside an owned temp dir)")
	configOut := flag.String("print-config", "", "also print the generated daemon config to this path (must be inside an owned temp dir)")
	flag.Parse()

	if *factsPath == "" {
		fmt.Fprintln(os.Stderr, "refused: --facts is required; this tool never probes or modifies a live host")
		os.Exit(2)
	}
	data, err := os.ReadFile(*factsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read facts:", err)
		os.Exit(2)
	}
	var facts native.HostFacts
	if err := json.Unmarshal(data, &facts); err != nil {
		fmt.Fprintln(os.Stderr, "decode facts:", err)
		os.Exit(2)
	}

	plan := native.BuildPlan(*nodeID, facts, time.Now().UTC())
	out := struct {
		NodeID       string                 `json:"node_id"`
		Mode         string                 `json:"mode"`
		Refusal      string                 `json:"refusal,omitempty"`
		Checks       []native.Check         `json:"checks"`
		Capabilities []string               `json:"capabilities"`
		UnitKind     string                 `json:"unit_kind,omitempty"`
		Launcher     string                 `json:"launcher,omitempty"`
		Report       interface{}            `json:"preflight"`
	}{
		NodeID:  plan.NodeID,
		Mode:    string(plan.Selection.Mode),
		Refusal: plan.Refusal,
		Checks:  plan.Selection.Checks,
	}
	if plan.Selection.Mode != native.ModeNone {
		out.UnitKind = plan.Daemon.UnitKind()
		out.Launcher = plan.Daemon.Launcher()
	}
	for _, c := range plan.Capabilities.GetCapabilities() {
		out.Capabilities = append(out.Capabilities, c.String())
	}
	out.Report = plan.Report
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode plan:", err)
		os.Exit(2)
	}
	fmt.Println(string(encoded))

	if *unitOut != "" || *configOut != "" {
		if plan.Refusal != "" {
			fmt.Fprintln(os.Stderr, "render manifest refused:", plan.Refusal)
			os.Exit(2)
		}
		manifest, err := plan.Daemon.BuildManifest()
		if err != nil {
			fmt.Fprintln(os.Stderr, "render manifest:", err)
			os.Exit(2)
		}
		for _, c := range native.CheckOptionParity(manifest.ConfigJSON, manifest.Unit) {
			if !c.Passed {
				fmt.Fprintln(os.Stderr, "option parity:", c.Code, c.Message)
				os.Exit(2)
			}
		}
		if *unitOut != "" {
			if err := os.WriteFile(*unitOut, []byte(manifest.Unit), 0o600); err != nil {
				fmt.Fprintln(os.Stderr, "write unit:", err)
				os.Exit(2)
			}
		}
		if *configOut != "" {
			if err := os.WriteFile(*configOut, []byte(manifest.ConfigJSON), 0o600); err != nil {
				fmt.Fprintln(os.Stderr, "write config:", err)
				os.Exit(2)
			}
		}
	}

	// Exercise the shared-contract version negotiation seam with a bounded
	// context so the runtime caller covers the whole composed path.
	r, err := native.NewRunner(native.TopologyDirectTLS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runner:", err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.Negotiate(ctx, &v1.VersionNegotiation{
		AgentVersion:        native.AgentVersion,
		ProtocolVersion:     native.ProtocolVersion,
		MinSupportedVersion: native.MinSupportedVersion,
		MaxSupportedVersion: native.MaxSupportedVersion,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "negotiate:", err)
		os.Exit(2)
	}
}
