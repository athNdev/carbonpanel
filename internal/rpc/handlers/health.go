// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/athNdev/carbon-panel/internal/health"
)

// NewHealthHandler returns a JSON health endpoint.
//
// ready is optional: when it is nil the endpoint is a pure liveness check and
// always reports ok; when non-nil it is invoked (e.g. a database ping) and a
// failure produces 503 with status "degraded". The endpoints are intentionally
// unauthenticated but expose no sensitive data.
func NewHealthHandler(started time.Time, ready func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		status := "ok"
		code := http.StatusOK
		if ready != nil {
			if err := ready(); err != nil {
				status = "degraded"
				code = http.StatusServiceUnavailable
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":        status,
			"uptimeSeconds": int(time.Since(started).Seconds()),
		})
	}
}

// NewLayeredHealthHandler returns the layered health endpoint. Unlike /readyz
// it never answers a green "ok" on behalf of a layer that could not be
// observed, and it reports a degraded Docker runtime without failing the
// request: a deployment must not restart the API because a remote node is down.
func NewLayeredHealthHandler(collect func(context.Context) health.Snapshot) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		snapshot := collect(ctx)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(snapshot.HTTPStatus())
		_ = json.NewEncoder(w).Encode(snapshot)
	}
}
