// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package operation

import (
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
)

func op(phase string) *db.ServerOperation {
	return &db.ServerOperation{
		ID:        "op-1",
		ServerID:  "srv-1",
		Kind:      "backup",
		Phase:     phase,
		CreatedAt: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 4, 10, 1, 0, 0, time.UTC),
	}
}

func TestDescribeSeparatesTheThreeOutcomes(t *testing.T) {
	cases := []struct {
		phase  string
		state  Outcome
		action NextAction
	}{
		{db.ServerOperationComplete, OutcomeSuccess, ActionNone},
		{db.ServerOperationFailed, OutcomeFailed, ActionRetryNewRequestID},
		{db.ServerOperationExecuting, OutcomeUnknown, ActionResolve},
		{db.ServerOperationPrepared, OutcomeUnknown, ActionWait},
		{"something-new", OutcomeUnknown, ActionContactOperator},
	}
	for _, tc := range cases {
		t.Run(tc.phase, func(t *testing.T) {
			view := Describe(op(tc.phase))
			if view.Outcome != tc.state {
				t.Errorf("outcome = %s, want %s", view.Outcome, tc.state)
			}
			if view.NextAction != tc.action {
				t.Errorf("next action = %s, want %s", view.NextAction, tc.action)
			}
			if view.Reason == "" {
				t.Error("reason is empty; every outcome must explain itself")
			}
		})
	}
}

func TestAnUncertainOutcomeIsNotReportedAsFailureOrSuccess(t *testing.T) {
	for _, phase := range []string{db.ServerOperationPrepared, db.ServerOperationExecuting} {
		view := Describe(op(phase))
		if view.Outcome == OutcomeSuccess || view.Outcome == OutcomeFailed {
			t.Fatalf("phase %s reported as %s, want unknown", phase, view.Outcome)
		}
	}
}

func TestDescribeCarriesTheEvidenceAnOperatorNeeds(t *testing.T) {
	o := op(db.ServerOperationExecuting)
	o.EndpointGeneration = 7
	o.ContainerID = "container-1"
	o.LastError = "daemon did not answer"
	view := Describe(o)

	if view.ID != "op-1" || view.ServerID != "srv-1" || view.Kind != "backup" {
		t.Errorf("identity lost: %+v", view)
	}
	if view.EndpointGeneration != 7 {
		t.Errorf("endpoint generation = %d, want 7", view.EndpointGeneration)
	}
	if !view.HasContainer {
		t.Error("HasContainer = false, want true")
	}
	if view.StartedAt.IsZero() || view.UpdatedAt.IsZero() {
		t.Error("timestamps lost")
	}
	if view.LastError != "daemon did not answer" {
		t.Errorf("last error = %q", view.LastError)
	}
}

func TestRedactErrorRemovesCredentials(t *testing.T) {
	cases := map[string]string{
		"dial tcp://admin:hunter2@10.0.0.5:2376: connection refused": "dial tcp://<redacted>@10.0.0.5:2376: connection refused",
		"https://user:token@registry.example/v2/ failed":             "https://<redacted>@registry.example/v2/ failed",
		"Authorization: Bearer abcdefghijklmnop failed":              "Authorization: Bearer <redacted> failed",
		"plain message with no secret":                               "plain message with no secret",
		"":                                                           "",
	}
	for input, want := range cases {
		if got := RedactError(input); got != want {
			t.Errorf("RedactError(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRedactErrorKeepsTheEvidence(t *testing.T) {
	got := RedactError("failed to remove server container abc123 on unix:///var/run/docker.sock: permission denied")
	for _, want := range []string{"abc123", "/var/run/docker.sock", "permission denied"} {
		if !strings.Contains(got, want) {
			t.Errorf("RedactError removed %q: %s", want, got)
		}
	}
}

func TestDescribeAllAndCountByOutcome(t *testing.T) {
	ops := []db.ServerOperation{
		*op(db.ServerOperationComplete),
		*op(db.ServerOperationFailed),
		*op(db.ServerOperationExecuting),
		*op(db.ServerOperationExecuting),
	}
	views := DescribeAll(ops)

	if len(views) != 4 {
		t.Fatalf("views = %d, want 4", len(views))
	}
	counts := CountByOutcome(views)
	if counts[OutcomeSuccess] != 1 || counts[OutcomeFailed] != 1 || counts[OutcomeUnknown] != 2 {
		t.Fatalf("counts = %+v, want 1 success, 1 failed, 2 unknown", counts)
	}
	if got := len(FilterOutcome(views, OutcomeUnknown)); got != 2 {
		t.Errorf("unknown views = %d, want 2", got)
	}
}

func TestDescribeHandlesAnEmptyOperation(t *testing.T) {
	if view := Describe(nil); view.ID != "" {
		t.Fatalf("Describe(nil) = %+v, want the zero view", view)
	}
}

func TestTrimRequestKey(t *testing.T) {
	if got := TrimRequestKey("short"); got != "short" {
		t.Errorf("TrimRequestKey(short) = %q", got)
	}
	long := strings.Repeat("a", 40)
	if got := TrimRequestKey(long); len([]rune(got)) > 13 {
		t.Errorf("TrimRequestKey(long) = %q, want it shortened", got)
	}
}
