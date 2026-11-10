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
	// OutcomeRunning means the mutation is still being attempted. It is the
	// only outcome that may still turn into a success or a failure on its own.
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
	// ActionWait means the operation is still in flight; wait for it.
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

// ExecutionStaleAfter is how long an executing operation may stay unchanged
// before it stops being "in flight" and becomes an unknown outcome. A mutation
// that has not recorded a completion by then may still be running, but the
// panel can no longer tell, and an operator needs the escape hatch.
const ExecutionStaleAfter = 2 * time.Minute

// ValidOutcome reports whether name is one this package produces. It exists so
// an RPC filter cannot silently match nothing because of a typo.
func ValidOutcome(name string) bool {
	switch Outcome(name) {
	case OutcomeSuccess, OutcomeFailed, OutcomeRunning, OutcomeUnknown:
		return true
	}
	return false
}

// View is the safe, redacted projection of one journal row. It carries what an
// operator needs to tell the outcomes apart and to pick a next step; it never
// carries credentials or a Docker host.
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
	HoldsLock          bool       `json:"holdsLock"`
	LastError          string     `json:"lastError,omitempty"`
	StartedAt          time.Time  `json:"startedAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	// RequestID is the client request id that keyed this row, when one was
	// supplied. It is the join key with activity events.
	RequestID string `json:"requestId,omitempty"`
}

// Describe projects a journal row for diagnostics, judged against the wall
// clock. Prefer DescribeAt where the caller already has a time.
func Describe(op *db.ServerOperation) View {
	return DescribeAt(op, time.Now())
}

// DescribeAt projects a journal row as it should be read at now. Whether an
// operation is still in flight depends on when it was last advanced, so the
// clock is a parameter instead of a hidden dependency.
func DescribeAt(op *db.ServerOperation, now time.Time) View {
	if op == nil {
		return View{}
	}
	outcome, action, reason := classify(op, now)
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
		HoldsLock:          op.ActiveServer != nil,
		LastError:          RedactError(op.LastError),
		StartedAt:          op.CreatedAt,
		UpdatedAt:          op.UpdatedAt,
		RequestID:          ClientRequestID(op),
	}
}

// ClientRequestID is the caller-supplied request id stored on a journal row.
// StorageKey concatenates that id with the kind; a generated key (empty
// request id) is returned as-is so an operator can still quote it.
func ClientRequestID(op *db.ServerOperation) string {
	if op == nil {
		return ""
	}
	key := strings.TrimSpace(op.RequestKey)
	if key == "" || op.Kind == "" {
		return key
	}
	suffix := ":" + op.Kind
	if strings.HasSuffix(key, suffix) {
		return strings.TrimSuffix(key, suffix)
	}
	return key
}

// DescribeAll projects every row, preserving order.
func DescribeAll(ops []db.ServerOperation) []View {
	return DescribeAllAt(ops, time.Now())
}

// DescribeAllAt projects every row at one instant, so a list never shows two
// rows judged by clocks that disagree.
func DescribeAllAt(ops []db.ServerOperation, now time.Time) []View {
	views := make([]View, 0, len(ops))
	for i := range ops {
		views = append(views, DescribeAt(&ops[i], now))
	}
	return views
}

// classify maps the durable phase to an outcome and the action that follows.
func classify(op *db.ServerOperation, now time.Time) (Outcome, NextAction, string) {
	switch op.Phase {
	case db.ServerOperationComplete:
		return OutcomeSuccess, ActionNone, "the operation completed and was recorded"
	case db.ServerOperationFailed:
		return OutcomeFailed, ActionRetryNewRequestID, "the attempt failed; the same request id is refused, so retry with a new one"
	case db.ServerOperationRecovered:
		return OutcomeUnknown, ActionNone, "the lifecycle lock was released without confirming the result; the container was observed at startup or an operator resolved it"
	case db.ServerOperationPrepared:
		return OutcomeUnknown, ActionWait, "the intent was recorded but the attempt had not started; the effect did not happen yet"
	case db.ServerOperationExecuting:
		if inFlight(op.UpdatedAt, now) {
			return OutcomeRunning, ActionWait, "the mutation is still being attempted; wait for it to finish"
		}
		return OutcomeUnknown, ActionResolve, "the attempt started but no completion was recorded; the external effect is unknown until it is observed"
	default:
		return OutcomeUnknown, ActionContactOperator, "the recorded phase is not one this panel recognises"
	}
}

// inFlight reports whether a row advanced at updatedAt could still be owned by
// a live process. A timestamp in the future counts as live: it means the clock
// moved, not that the operation is abandoned.
func inFlight(updatedAt, now time.Time) bool {
	if updatedAt.IsZero() {
		return false
	}
	age := now.Sub(updatedAt)
	if age < 0 {
		return true
	}
	return age <= ExecutionStaleAfter
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
