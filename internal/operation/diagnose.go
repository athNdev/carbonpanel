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

// UncertainOutcomeMarker is recorded on a journal row whose external effect
// could not be observed. It makes Describe return unknown immediately instead
// of waiting for the stale-execution timeout.
const UncertainOutcomeMarker = "external effect is uncertain"

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

// ReasonCode is the stable classification behind an outcome. Two rows can both
// be "unknown" for different reasons, and a UI or runbook needs to tell a stale
// attempt from an operation that never started without parsing prose.
type ReasonCode string

const (
	// ReasonRunning means the mutation is still being attempted.
	ReasonRunning ReasonCode = "running"
	// ReasonCompleted means the mutation completed and was recorded.
	ReasonCompleted ReasonCode = "completed"
	// ReasonAttemptFailed means the mutation returned an error and was recorded.
	ReasonAttemptFailed ReasonCode = "attempt_failed"
	// ReasonRecovered means the lock was released without confirming the effect.
	ReasonRecovered ReasonCode = "recovered_without_confirmation"
	// ReasonPreparedNotStarted means the intent was recorded but the attempt had
	// not begun, so the external effect did not happen yet.
	ReasonPreparedNotStarted ReasonCode = "prepared_not_started"
	// ReasonUncertainEffect means the mutation reached an external effect that
	// was not observed. Retrying blindly could repeat it.
	ReasonUncertainEffect ReasonCode = "uncertain_external_effect"
	// ReasonExecutingStale means the attempt stopped advancing before recording
	// a completion. It may still be running, so it is not a failure to retry.
	ReasonExecutingStale ReasonCode = "executing_stale"
	// ReasonUnrecognisedPhase means the stored phase is not one this panel knows.
	ReasonUnrecognisedPhase ReasonCode = "unrecognised_phase"
)

// Correlation is a derived, non-secret statement about whether a journal row
// still belongs to the node endpoint it recorded. It never carries the Docker
// daemon id itself: an operator can join the row to a server/node and see
// whether the recorded endpoint still matches without seeing a host or a
// daemon fingerprint.
type Correlation string

const (
	// CorrelationMatched means the recorded node endpoint and daemon identity
	// are present and still match the current node.
	CorrelationMatched Correlation = "matched"
	// CorrelationUnmatched means the node, endpoint generation or daemon
	// identity recorded on the operation differs from the current node.
	CorrelationUnmatched Correlation = "unmatched"
	// CorrelationUnknown means the identity was not observed on one side, so a
	// match cannot be claimed either way.
	CorrelationUnknown Correlation = "unknown"
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
	ID                 string      `json:"id"`
	ServerID           string      `json:"serverId"`
	NodeID             string      `json:"nodeId,omitempty"`
	Kind               string      `json:"kind"`
	Phase              string      `json:"phase"`
	Outcome            Outcome     `json:"outcome"`
	NextAction         NextAction  `json:"nextAction"`
	ReasonCode         ReasonCode  `json:"reasonCode"`
	Reason             string      `json:"reason"`
	Correlation        Correlation `json:"correlation"`
	EndpointGeneration int64       `json:"endpointGeneration"`
	HasContainer       bool        `json:"hasContainer"`
	HoldsLock          bool        `json:"holdsLock"`
	LastError          string      `json:"lastError,omitempty"`
	StartedAt          time.Time   `json:"startedAt"`
	UpdatedAt          time.Time   `json:"updatedAt"`
	// RequestID is the client request id that keyed this row, when one was
	// supplied. It is the join key with activity events.
	RequestID string `json:"requestId,omitempty"`
	// RequestKey is the stable journal key (client request id plus kind, or a
	// generated key). It is non-secret and lets an operator quote exactly the
	// row the journal matched on.
	RequestKey string `json:"requestKey,omitempty"`
}

// Describe projects a journal row for diagnostics, judged against the wall
// clock. Prefer DescribeAt where the caller already has a time.
func Describe(op *db.ServerOperation) View {
	return DescribeAt(op, time.Now())
}

// DescribeAt projects a journal row as it should be read at now. Whether an
// operation is still in flight depends on when it was last advanced, so the
// clock is a parameter instead of a hidden dependency. Without a node identity
// the correlation is unknown; use DescribeAtForNode when the current node is
// available.
func DescribeAt(op *db.ServerOperation, now time.Time) View {
	return DescribeAtForNode(op, nil, now)
}

// DescribeAtForNode projects a journal row against the node it recorded. The
// node is only used to derive a matched/unmatched/unknown correlation; the
// projection still never carries a daemon id, host or credential.
func DescribeAtForNode(op *db.ServerOperation, node *db.Node, now time.Time) View {
	if op == nil {
		return View{}
	}
	outcome, action, code, reason := classify(op, now)
	return View{
		ID:                 op.ID,
		ServerID:           op.ServerID,
		NodeID:             op.NodeID,
		Kind:               op.Kind,
		Phase:              op.Phase,
		Outcome:            outcome,
		NextAction:         action,
		ReasonCode:         code,
		Reason:             reason,
		Correlation:        Correlate(op, node),
		EndpointGeneration: op.EndpointGeneration,
		HasContainer:       op.ContainerID != "",
		HoldsLock:          op.ActiveServer != nil,
		LastError:          RedactError(op.LastError),
		StartedAt:          op.CreatedAt,
		UpdatedAt:          op.UpdatedAt,
		RequestID:          ClientRequestID(op),
		RequestKey:         strings.TrimSpace(op.RequestKey),
	}
}

// Correlate compares the identity recorded on an operation with the node's
// current identity and returns only a derived state. A missing daemon identity
// is unknown, never a match: the panel must not claim an endpoint it has not
// observed.
func Correlate(op *db.ServerOperation, node *db.Node) Correlation {
	if op == nil || node == nil {
		return CorrelationUnknown
	}
	if strings.TrimSpace(op.NodeID) == "" {
		return CorrelationUnknown
	}
	if node.ID != op.NodeID {
		return CorrelationUnmatched
	}
	if op.EndpointGeneration > 0 && node.EndpointGeneration > 0 &&
		op.EndpointGeneration != node.EndpointGeneration {
		return CorrelationUnmatched
	}
	if op.DaemonID != "" && node.DaemonID != "" && op.DaemonID != node.DaemonID {
		return CorrelationUnmatched
	}
	if op.DaemonID == "" || node.DaemonID == "" ||
		op.EndpointGeneration == 0 || node.EndpointGeneration == 0 {
		return CorrelationUnknown
	}
	return CorrelationMatched
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
	return DescribeAllAtForNodes(ops, nil, now)
}

// DescribeAllAtForNodes projects every row at one instant with a node lookup for
// the derived correlation. A missing node leaves the correlation unknown.
func DescribeAllAtForNodes(ops []db.ServerOperation, nodes map[string]*db.Node, now time.Time) []View {
	views := make([]View, 0, len(ops))
	for i := range ops {
		views = append(views, DescribeAtForNode(&ops[i], nodes[ops[i].NodeID], now))
	}
	return views
}

// classify maps the durable phase to an outcome, the action that follows and
// the stable reason code. Reason is the human sentence; ReasonCode is what a
// caller branches on.
func classify(op *db.ServerOperation, now time.Time) (Outcome, NextAction, ReasonCode, string) {
	switch op.Phase {
	case db.ServerOperationComplete:
		return OutcomeSuccess, ActionNone, ReasonCompleted, "the operation completed and was recorded"
	case db.ServerOperationFailed:
		return OutcomeFailed, ActionRetryNewRequestID, ReasonAttemptFailed, "the attempt failed; the same request id is refused, so retry with a new one"
	case db.ServerOperationRecovered:
		return OutcomeUnknown, ActionNone, ReasonRecovered, "the lifecycle lock was released without confirming the result; the container was observed at startup or an operator resolved it"
	case db.ServerOperationPrepared:
		return OutcomeUnknown, ActionWait, ReasonPreparedNotStarted, "the intent was recorded but the attempt had not started; the effect did not happen yet"
	case db.ServerOperationExecuting:
		// An explicit uncertainty note is a first-class unknown even while the
		// process is alive: the mutation reached an external effect that was
		// not observed, and a blind retry could repeat it.
		if strings.Contains(op.LastError, UncertainOutcomeMarker) {
			return OutcomeUnknown, ActionResolve, ReasonUncertainEffect, RedactError(op.LastError)
		}
		if inFlight(op.UpdatedAt, now) {
			return OutcomeRunning, ActionWait, ReasonRunning, "the mutation is still being attempted; wait for it to finish"
		}
		return OutcomeUnknown, ActionResolve, ReasonExecutingStale, "the attempt started but no completion was recorded; the external effect is unknown until it is observed"
	default:
		return OutcomeUnknown, ActionContactOperator, ReasonUnrecognisedPhase, "the recorded phase is not one this panel recognises"
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
