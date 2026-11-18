// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370): registration-time exclusion policy.
//
// Tools that the OSS-375 audit classifies OUT OF SCOPE are never inserted into
// a production tool set. The policy is attached to the server instance by
// NewServer before any add* helper runs, so no excluded tool is momentarily
// mounted and then removed. Family slice tests build isolated servers that are
// deliberately not policy-bound; they verify the family hand-off itself.
package mcp

import (
	"sync"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var registrationPolicies sync.Map // map[*sdkmcp.Server]map[string]bool

// bindRegistrationPolicy attaches the combined allowlist to a server before
// its tools are registered. The allowlist is the single generated catalog.
func bindRegistrationPolicy(s *sdkmcp.Server) map[string]bool {
	allowed := make(map[string]bool, len(exposedTools))
	for _, spec := range exposedTools {
		allowed[spec.Name] = true
	}
	registrationPolicies.Store(s, allowed)
	return allowed
}

// toolAllowedOn reports whether a tool may be added to s. Only production
// servers created by NewServer are policy-bound; isolated family slice tests
// keep their own complete hand-off set.
func toolAllowedOn(s *sdkmcp.Server, name string) bool {
	v, ok := registrationPolicies.Load(s)
	if !ok {
		return true
	}
	return v.(map[string]bool)[name]
}

// IntegrationAllowedTools returns the allowlist bound to s, for tests that
// must assert a tool was never inserted rather than merely removed later.
func IntegrationAllowedTools(s *sdkmcp.Server) map[string]bool {
	v, ok := registrationPolicies.Load(s)
	if !ok {
		return nil
	}
	out := make(map[string]bool, len(v.(map[string]bool)))
	for k, ok := range v.(map[string]bool) {
		out[k] = ok
	}
	return out
}
