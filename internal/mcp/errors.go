// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
)

// ToolError is the structured error payload returned in StructuredContent for
// every failed tool call. Kind is stable so clients can branch on it.
type ToolError struct {
	Kind              string `json:"kind" jsonschema:"stable error kind"`
	Code              string `json:"code,omitempty" jsonschema:"Connect status code when applicable"`
	Message           string `json:"message" jsonschema:"human readable explanation"`
	Tool              string `json:"tool" jsonschema:"tool name"`
	Procedure         string `json:"procedure" jsonschema:"Connect procedure"`
	MinimumPanelBuild string `json:"minimum_panel_build,omitempty" jsonschema:"minimum controller build for capability errors"`
}

// Error implements error so tool implementations can return a ToolError
// directly for argument or confirmation failures.
func (e *ToolError) Error() string { return e.Message }

const (
	ErrKindCapability           = "capability_unavailable"
	ErrKindNotFound             = "not_found"
	ErrKindPermissionDenied     = "permission_denied"
	ErrKindUnauthenticated      = "unauthenticated"
	ErrKindConfirmationRequired = "confirmation_required"
	ErrKindInvalidArguments     = "invalid_arguments"
	ErrKindUpstream             = "upstream_error"
)

// toToolError maps Connect and local errors to a structured tool error.
//
// Only an Unimplemented code (including Connect's HTTP 404 -> Unimplemented
// mapping for an unregistered procedure) or a NotFound whose message explicitly
// identifies a transport-level missing procedure is a capability gap. A plain
// NotFound is a domain record absence and must be reported honestly as such:
// a missing server/file/world is never an instruction to upgrade the panel.
func toToolError(spec ToolSpec, err error) ToolError {
	var te *ToolError
	if errors.As(err, &te) {
		return *te
	}
	base := ToolError{Tool: spec.Name, Procedure: spec.Procedure}
	var ce *connect.Error
	if errors.As(err, &ce) {
		base.Code = ce.Code().String()
		switch ce.Code() {
		case connect.CodeUnimplemented:
			return capabilityError(base, spec, ce.Message())
		case connect.CodeNotFound:
			if isTransportMissingProcedure(ce.Message()) {
				return capabilityError(base, spec, ce.Message())
			}
			base.Kind = ErrKindNotFound
			base.Message = fmt.Sprintf("%s: requested record does not exist on the controller (%s). This is not a panel capability issue.", spec.Procedure, ce.Message())
			return base
		case connect.CodePermissionDenied:
			base.Kind = ErrKindPermissionDenied
			base.Message = fmt.Sprintf("the API token lacks permission for %s: %s", spec.Procedure, ce.Message())
			return base
		case connect.CodeUnauthenticated:
			base.Kind = ErrKindUnauthenticated
			base.Message = fmt.Sprintf("the controller rejected the API token for %s: %s", spec.Procedure, ce.Message())
			return base
		}
	}
	base.Kind = ErrKindUpstream
	base.Message = strings.TrimSpace(fmt.Sprintf("%s failed: %v", spec.Procedure, err))
	return base
}

func capabilityError(base ToolError, spec ToolSpec, detail string) ToolError {
	base.Kind = ErrKindCapability
	base.MinimumPanelBuild = string(DeclaredBuild(spec))
	base.Message = fmt.Sprintf(
		"tool %q requires panel build %s containing %s; the current panel reported an unimplemented procedure (%s). The tool is available in every build of this MCP server; upgrade the controller to use it.",
		spec.Name, base.MinimumPanelBuild, spec.Procedure, detail,
	)
	return base
}

// isTransportMissingProcedure recognizes the explicit transport-level markers
// used by Connect/HTTP gateways for an unknown service or method. Domain code
// must not use these phrases in a not-found message.
func isTransportMissingProcedure(message string) bool {
	lower := strings.ToLower(message)
	for _, marker := range []string{
		"unknown service", "unknown method", "unknown procedure",
		"no such procedure", "not implemented", "unimplemented",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// DeclaredBuild returns the minimum panel build recorded in the catalog for a
// tool. It falls back to the stable build for unknown tools so the error shape
// is always populated.
func DeclaredBuild(spec ToolSpec) PanelBuild {
	if entry, ok := Declared(spec.Name); ok && entry.Build != "" {
		return entry.Build
	}
	return PanelBuildStableV140
}
