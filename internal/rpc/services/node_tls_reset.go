// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
)

// NodeTLSClearConfirmationPhrase is the exact confirmation required by the
// Clear TLS credentials action. It is not a credential and may be logged.
const NodeTLSClearConfirmationPhrase = "CLEAR TLS CREDENTIALS"

// ErrNodeTLSClearRefused is returned when the named clear action refuses a
// request. It is never returned by ordinary node edits.
var ErrNodeTLSClearRefused = errors.New("node TLS credential clear refused")

// NodeTLSClearAction carries the authenticated operator context and explicit
// confirmation for a credential clear. OperatorCanUpdateNode must be populated
// by the permission-enforcing boundary from an enforced nodes:update grant on
// the target node; nodes:read alone never authorizes this action. The dedicated
// wire RPC is a coordinated follow-up because proto work is gated until
// P0-GATE and both halves ship. See docs/adr/0018-node-response-redaction.md.
type NodeTLSClearAction struct {
	OperatorID            string
	OperatorCanUpdateNode bool
	TargetNodeName        string
	ConfirmationPhrase    string
}

// ClearNodeTLSCredentials is the only sanctioned path that removes stored
// Docker daemon TLS material. It is deliberately not reachable from a redacted
// read response or from an ordinary UpdateNode edit. Scope is one named node and
// three credential fields; no world, backup, database, daemon data or other
// node is touched.
func (s *NodeService) ClearNodeTLSCredentials(ctx context.Context, nodeID string, action NodeTLSClearAction) error {
	if nodeID == "" {
		return fmt.Errorf("%w: node ID is required", ErrNodeTLSClearRefused)
	}
	if action.OperatorID == "" {
		return fmt.Errorf("%w: operator identity is required", ErrNodeTLSClearRefused)
	}
	if !action.OperatorCanUpdateNode {
		return fmt.Errorf("%w: node-management permission required", ErrNodeTLSClearRefused)
	}
	if action.TargetNodeName == "" {
		return fmt.Errorf("%w: target node name confirmation is required", ErrNodeTLSClearRefused)
	}
	if action.ConfirmationPhrase != NodeTLSClearConfirmationPhrase {
		return fmt.Errorf("%w: confirmation phrase mismatch", ErrNodeTLSClearRefused)
	}

	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("%w: node lookup failed: %v", ErrNodeTLSClearRefused, err)
	}
	if node.Name != action.TargetNodeName {
		return fmt.Errorf("%w: target node name mismatch", ErrNodeTLSClearRefused)
	}
	if node.TLSCACert == "" && node.TLSCert == "" && node.TLSKey == "" {
		// Nothing to clear. Do not bump the generation for a no-op.
		return nil
	}

	before := node.EndpointGeneration
	node.TLSCACert = ""
	node.TLSCert = ""
	node.TLSKey = ""
	if err := s.store.UpdateNode(ctx, node); err != nil {
		return fmt.Errorf("%w: persistence failed: %v", ErrNodeTLSClearRefused, err)
	}
	if s.pool != nil {
		_ = s.pool.RemoveNode(node.ID)
	}
	// Audit records identity and endpoint change only, never credential values.
	auditCtx := ctx
	if auth.GetUserFromContext(auditCtx) == nil {
		auditCtx = auth.WithUser(ctx, &auth.AuthenticatedUser{ID: action.OperatorID})
	}
	writeAudit(auditCtx, s.store, s.log, auditFields{
		Event:      activity.EventNodeTLSClear,
		SubjectTyp: "node",
		SubjectID:  node.ID,
	}, map[string]any{
		"generation_before":         before,
		"generation_after":          node.EndpointGeneration,
		"credential_fields_cleared": "tls_ca_cert,tls_cert,tls_key",
	})
	s.log.Info("node.tls_credentials_cleared action=clear_tls_credentials operator=%s node=%s generation=%d->%d",
		action.OperatorID, node.ID, before, node.EndpointGeneration)
	return nil
}
