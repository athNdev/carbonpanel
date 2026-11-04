// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package operation

import (
	"regexp"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
)

// Outcome is what an operator should understand an operation to have done.
// Unknown is a first-class answer: a mutation whose external effect was not
// recorded is neither a success nor a failure.
type Outcome string

const (
	// OutcomeSuccess means the mutation completed and was recorded.
	OutcomeSuccess Outcome = "success"
	// OutcomeFailed means the mutation returned an error and was recorded.
	OutcomeFailed Outcome = "failed"
	// OutcomeRunning means the mutation is still being attempted.
	OutcomeRunning Outcome = "running"
	// OutcomeUnknown means the intent was recorded but no completion or
	// failure was. The external effect may or may not have happened.
	OutcomeUnknown Outcome = "unknown"
)

// NextAction is the safe next step for an operation in its current state. It
// is a stable code so a UI, a runbook or a support engineer can agree on the
// remedy without parsing an error string.
type NextAction string

const (
	// ActionNone means nothing is required.
	ActionNone NextAction = "none"
	// ActionWait means the operation is still running; wait for it to finish.
	ActionWait NextAction = "wait"
	// ActionResolve means the process that owned the operation is gone. Startup
	// recovery observes the container before releasing the lock; an operator can
	// also resolve it explicitly.
	ActionResolve NextAction = "resolve_operation"
	// ActionRetryNewRequestID means the recorded attempt failed and the same
	// request id is refused, so a retry needs a new one.
	ActionRetryNewRequestID NextAction = "retry_with_new_request_id"
	// ActionContactOperator means the state is outside the automatic paths.
	ActionContactOperator NextAction = "contact_operator"
)

// View is the safe, redacted projection of one journal row. It carries what an
// operator needs to tell the three outcomes apart and to pick a next step; it
// never carries credentials or a Docker host.
type View struct {
	ID                 string     `json:"id"`
	ServerID           string     `json:"serverId"`
	Kind               string     `json:"kind"`
	Phase              string     `json:"phase"`
	Outcome            Outcome    `json:"outcome"`
	NextAction         NextAction `json:"nextAction"`
	Reason             string     `json:"reason"`
	EndpointGeneration int64      `json:"endpointGeneration"`
	HasContainer       bool       `json:"hasContainer"`
	LastError          string     `json:"lastError,omitempty"`
	StartedAt          time.Time  `json:"startedAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

// Describe projects a journal row for diagnostics.
func Describe(op *db.ServerOperation) View {
	if op == nil {
		return View{}
	}
	outcome, action, reason := classify(op)
	return View{
		ID:                 op.ID,
		ServerID:           op.ServerID,
		Kind:               op.Kind,
		Phase:              op.Phase,
		Outcome:            outcome,
		NextAction:         action,
		Reason:             reason,
		EndpointGeneration: op.EndpointGeneration,
		HasContainer:       op.ContainerID != "",
		LastError:          RedactError(op.LastError),
		StartedAt:          op.CreatedAt,
		UpdatedAt:          op.UpdatedAt,
	}
}

// DescribeAll projects every row, preserving order.
func DescribeAll(ops []db.ServerOperation) []View {
	views := make([]View, 0, len(ops))
	for i := range ops {
		views = append(views, Describe(&ops[i]))
	}
	return views
}

// classify maps the durable phase to an outcome and the action that follows.
func classify(op *db.ServerOperation) (Outcome, NextAction, string) {
	switch op.Phase {
	case db.ServerOperationComplete:
		return OutcomeSuccess, ActionNone, "the operation completed and was recorded"
	case db.ServerOperationFailed:
		return OutcomeFailed, ActionRetryNewRequestID, "the attempt failed; the same request id is refused, so retry with a new one"
	case db.ServerOperationPrepared:
		return OutcomeUnknown, ActionWait, "the intent was recorded but the attempt had not started; the effect did not happen yet"
	case db.ServerOperationExecuting:
		return OutcomeUnknown, ActionResolve, "the attempt started but no completion was recorded; the external effect is unknown until it is observed"
	default:
		return OutcomeUnknown, ActionContactOperator, "the recorded phase is not one this panel recognises"
	}
}

// credentialURL matches a scheme://user:password@host prefix, which is the only
// place a Docker host string carries a secret.
var credentialURL = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^\s/@]+@`)

// bearerToken matches an inline bearer or basic credential in a header dump.
var bearerToken = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)

// RedactError removes credentials from daemon and client error text before it
// is written to a diagnostic. Hosts, paths and messages are kept: they are the
// evidence an operator needs.
func RedactError(text string) string {
	if text == "" {
		return ""
	}
	redacted := credentialURL.ReplaceAllString(text, "$1<redacted>@")
	return bearerToken.ReplaceAllString(redacted, "$1 <redacted>")
}

// CountByOutcome counts the projected operations by outcome, which is what a
// bundle header and a health line want.
func CountByOutcome(views []View) map[Outcome]int {
	counts := map[Outcome]int{}
	for _, v := range views {
		counts[v.Outcome]++
	}
	return counts
}

// FilterOutcome returns the views with the given outcome.
func FilterOutcome(views []View, outcome Outcome) []View {
	var out []View
	for _, v := range views {
		if v.Outcome == outcome {
			out = append(out, v)
		}
	}
	return out
}

// TrimRequestKey shortens a client request key for display without changing the
// value the journal matches on.
func TrimRequestKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 12 {
		return key
	}
	return key[:12] + "…"
}
