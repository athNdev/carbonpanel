// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/nodescan"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// NodeScanResponse is the JSON payload for GET /api/v1/nodes/scan.
type NodeScanResponse struct {
	Candidates []docker.DockerCandidate `json:"candidates"`
	KnownNodes int                      `json:"knownNodes"`
	ScannedAt  string                   `json:"scannedAt"`
}

// NewNodeScanHandler creates an HTTP handler that auto-detects active Docker
// daemons on all available network interfaces by probing the local unix
// socket plus TCP 2375/2376 across interface addresses. It complements the
// per-node PingNode RPC: the scan finds candidate daemons, PingNode verifies
// registered ones.
func NewNodeScanHandler(store *storage.Store, authManager *auth.Manager, enforcer *rbac.Enforcer, log *logger.Logger) http.Handler {
	scanner := nodescan.New(store, log)
	return nodeScanHandler(scanner.Scan, authManager, enforcer)
}

func nodeScanHandler(scan func(context.Context) *v1.ScanNodesResponse, authManager *auth.Manager, enforcer *rbac.Enforcer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Match the generated method: authenticate even in explicit no-auth
		// mode, then enforce nodes/read on the global object. Missing policy
		// dependencies must fail closed rather than bypassing the check.
		if authManager == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		user, err := authManager.AuthenticateFromHeader(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if enforcer == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		allowed, rbacErr := enforcer.Enforce(user.Roles, rbac.ResourceNodes, rbac.ActionRead, "*")
		if rbacErr != nil || !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		result := scan(r.Context())
		var candidates []docker.DockerCandidate
		for _, candidate := range result.Candidates {
			candidates = append(candidates, docker.DockerCandidate{Host: candidate.Host, Port: int(candidate.Port), Reachable: candidate.Reachable, LatencyMs: candidate.LatencyMs, Source: candidate.Source})
		}

		writeJSON(w, http.StatusOK, NodeScanResponse{
			Candidates: candidates,
			KnownNodes: int(result.KnownNodes),
			ScannedAt:  result.ScannedAt.AsTime().Format(time.RFC3339),
		})
	})
}
