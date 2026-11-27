// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import type { LifecycleOperation } from '$lib/proto/carbonpanel/v1/operation_pb';

/** The Carbon tag colour that reads as this outcome at a glance. */
export type OutcomeTagType = 'green' | 'red' | 'blue' | 'yellow' | 'gray';

/**
 * An unknown outcome is amber, not red: it is genuinely undecided, and colouring
 * it as a failure pushes people into a blind retry of something that may have
 * already happened.
 */
export function outcomeTagType(outcome: string): OutcomeTagType {
	switch (outcome) {
		case 'success':
			return 'green';
		case 'failed':
			return 'red';
		case 'running':
			return 'blue';
		case 'unknown':
			return 'yellow';
		default:
			return 'gray';
	}
}

/**
 * Correlation is derived by the backend and never contains a daemon id. Green
 * means the recorded node endpoint still matches; red means it does not; grey
 * means the panel could not compare daemon identity either way.
 */
export function correlationTagType(correlation: string): OutcomeTagType {
	switch (correlation) {
		case 'matched':
			return 'green';
		case 'unmatched':
			return 'red';
		default:
			return 'gray';
	}
}

/**
 * A stale attempt and an uncertain external effect are both unknown outcomes:
 * the mutation may already have happened. Retrying blindly can repeat it, so
 * the UI has to say so instead of treating the row as a failure.
 */
export function requiresManualCheck(
	op: Pick<LifecycleOperation, 'outcome' | 'reasonCode'>
): boolean {
	return (
		op.outcome === 'unknown' &&
		(op.reasonCode === 'executing_stale' || op.reasonCode === 'uncertain_external_effect')
	);
}

/** The sentence shown in the "next step" column. */
export function operationActionLabel(action: string): string {
	switch (action) {
		case 'retry_with_new_request_id':
			return 'Retry with a new request id';
		case 'resolve_operation':
			return 'Release the lock';
		case 'wait':
			return 'Wait for it to finish';
		case 'contact_operator':
			return 'Contact an operator';
		default:
			return 'Nothing to do';
	}
}

/**
 * Releasing a lock is only offered when the server is actually blocked and the
 * journal itself says the safe move is to release it. Offering it on a
 * successful or failed row would invite needless clicks on a settled server.
 */
export function canReleaseLock(op: Pick<LifecycleOperation, 'holdsLock' | 'nextAction'>): boolean {
	return op.holdsLock && op.nextAction === 'resolve_operation';
}

/** Counts by outcome, for the summary tags above the table. */
export function countOperationsByOutcome(
	operations: ReadonlyArray<Pick<LifecycleOperation, 'outcome'>>
): Record<string, number> {
	const counts: Record<string, number> = {};
	for (const op of operations) {
		counts[op.outcome] = (counts[op.outcome] ?? 0) + 1;
	}
	return counts;
}

/**
 * The request key column shows the journal key and falls back to the client
 * request id on partial rows. The tooltip has to name the value that is on
 * screen: a generated key is a request key, not a request id.
 */
export function requestKeyTitle(
	op: Pick<LifecycleOperation, 'requestKey' | 'requestId'>
): string | undefined {
	if (op.requestKey) return `request key: ${op.requestKey}`;
	if (op.requestId) return `request id: ${op.requestId}`;
	return undefined;
}
