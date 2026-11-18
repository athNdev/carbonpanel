// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370): production wiring of every family
// hand-off into one combined server. This file owns registration only; family
// files stay untouched. The SDK de-duplicates tools by name, so a duplicate
// procedure promoted from more than one family resolves to the first-slice or
// highest-priority handler without ever double-mounting a tool.
package mcp

import (
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerCombinedTools mounts the pinned first slice, all four family
// hand-offs and the integration-owned additions. It is the single production
// entry point used by NewServer and by the combined fixture.
func registerCombinedTools(s *sdkmcp.Server, c *Clients) error {
	if c == nil {
		return fmt.Errorf("mcp: nil clients")
	}
	registerTools(s, c)
	serverFam, contentFam, adminFam, streamFam, err := c.familyClients()
	if err != nil {
		return err
	}
	RegisterFamilyServerTools(s, serverFam)
	registerContentTools(s, contentFam)
	RegisterFamilyAdminTools(s, adminFam)
	// The stream family contributes typed diagnostic helpers and audit
	// evidence, not standalone MCP tools: its raw byte/session surfaces are
	// deliberately excluded, and its typed procedures are already mounted by
	// the server/content/admin registration above. Keep the client set alive
	// so the connection and auth policy stay shared across families.
	_ = streamFam
	RegisterIntegrationTools(s, c)

	// The family registration helpers also mount their excluded descriptor
	// tools (for example free-form console commands, node TLS dumps and
	// third-party log uploads). OSS-375 classifies those OUT OF SCOPE, so the
	// single generated catalog omits them. Remove them after registration to
	// keep the live tool set identical to exposedTools on every panel build.
	// This is a policy filter only: no family source is edited and no
	// supported tool is removed.
	combined := make(map[string]bool, len(exposedTools))
	for _, spec := range exposedTools {
		combined[spec.Name] = true
	}
	var excluded []string
	for _, d := range FamilyServerDescriptors {
		if d.Name != "" && !combined[d.Name] {
			excluded = append(excluded, d.Name)
		}
	}
	for _, d := range ContentToolDescriptors {
		if d.Tool != "" && !combined[d.Tool] {
			excluded = append(excluded, d.Tool)
		}
	}
	for _, d := range FamilyAdminDescriptors {
		if d.Tool != "" && !combined[d.Tool] {
			excluded = append(excluded, d.Tool)
		}
	}
	if len(excluded) != 0 {
		s.RemoveTools(excluded...)
	}
	return nil
}
