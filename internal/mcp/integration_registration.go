// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370): production wiring of every family
// hand-off into one combined server. The OSS-375 exclusion policy is bound to
// the server before any add* helper runs, so a secret-bootstrap, free-form
// command or egress tool is never inserted into the live tool set — not even
// momentarily.
package mcp

import (
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerCombinedTools mounts the family hand-offs and the integration-owned
// additions. It is the single production entry point used by NewServer and by
// the combined fixture.
//
// Registration order is deliberate. go-sdk featureSet.add REPLACES a tool with
// the same name (last wins), so the sources are mounted in REVERSE generator
// priority: integration (lowest), admin, content, server, first slice
// (highest). The last-wins live handler therefore matches the generator's
// dedup winner exactly — including the six ConfigService names declared by
// both the server and admin families. Never reorder this function without
// also changing cataloggen sourcePriority.
func registerCombinedTools(s *sdkmcp.Server, c *Clients) error {
	if c == nil {
		return fmt.Errorf("mcp: nil clients")
	}
	// Bind the allowlist BEFORE the first add* call. Excluded tools are
	// refused by toolAllowedOn at insertion time; no post-registration
	// removal is needed or permitted.
	bindRegistrationPolicy(s)

	serverFam, contentFam, adminFam, err := c.familyClients()
	if err != nil {
		return err
	}

	RegisterIntegrationTools(s, c)
	// OSS-387: the four scoped_stream_* tools are now advertised from the
	// production constructor. They carry bounded frames only through the
	// authenticated controller FileService RPCs; the gateway owns no panel or
	// node storage handle. The registration policy bound above is the single
	// allowlist, so a tool is mounted only when the generated catalog declares
	// it. Never remove this call to re-take the path-B hold.
	RegisterStreamingTools(s, c)
	// OSS-370/371-375 byte-transport lanes. Both are now mounted from the
	// production constructor (previously they were only called in isolated
	// fixtures). Every call reaches the controller through a generated Connect
	// client behind the production auth interceptor: download stages a file and
	// reads bounded controller frames; upload stages owned sessions and commits
	// through a fresh server-scoped files:create[server_id] check. No REST client
	// and no context-value transport is involved. bindRegistrationPolicy above
	// is the single allowlist, so a tool is mounted only when the generated
	// catalog declares it (see the policy guard in addDownloadTool/uploadTool).
	RegisterDownloadTools(s, c)
	RegisterUploadFamilyTools(s, c)
	RegisterFamilyAdminTools(s, adminFam)
	registerContentTools(s, contentFam)
	RegisterFamilyServerTools(s, serverFam)
	registerTools(s, c) // first slice wins last; see sourcePriority
	return nil
}
