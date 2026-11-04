// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/health"
)

func layeredSnapshot(db, runtime, routes health.State) health.Snapshot {
	return health.Compute(health.Input{
		StartedAt:      time.Now().Add(-time.Minute),
		CheckedAt:      time.Now(),
		StaleAfter:     time.Minute,
		Database:       health.Observation{State: db},
		Runtime:        health.Observation{State: runtime},
		Routes:         health.Observation{State: routes},
		RunningStatus:  "running",
		StartingStatus: "creating",
	})
}

func TestLayeredHealthHandlerReportsEveryLayer(t *testing.T) {
	h := NewLayeredHealthHandler(func(context.Context) health.Snapshot {
		return layeredSnapshot(health.StateOK, health.StateOK, health.StateOK)
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/health/detail", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	var body health.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body %q: %v", rec.Body.String(), err)
	}
	if body.Status != health.StateOK {
		t.Errorf("status = %s, want ok", body.Status)
	}
	if len(body.Components) != 5 {
		t.Errorf("components = %d, want 5: %s", len(body.Components), rec.Body.String())
	}
}

func TestLayeredHealthHandlerKeepsTheAPIAvailableWhenARuntimeIsDown(t *testing.T) {
	h := NewLayeredHealthHandler(func(context.Context) health.Snapshot {
		return layeredSnapshot(health.StateOK, health.StateUnavailable, health.StateOK)
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/health/detail", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a runtime outage must not fail the API probe", rec.Code)
	}
	var body health.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body: %v", err)
	}
	if body.Status != health.StateUnavailable {
		t.Errorf("reported status = %s, want unavailable", body.Status)
	}
}

func TestLayeredHealthHandlerFailsWhenTheDatabaseIsDown(t *testing.T) {
	h := NewLayeredHealthHandler(func(context.Context) health.Snapshot {
		return layeredSnapshot(health.StateUnavailable, health.StateOK, health.StateOK)
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/health/detail", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestLayeredHealthHandlerRejectsNonGet(t *testing.T) {
	h := NewLayeredHealthHandler(func(context.Context) health.Snapshot {
		return layeredSnapshot(health.StateOK, health.StateOK, health.StateOK)
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/health/detail", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
