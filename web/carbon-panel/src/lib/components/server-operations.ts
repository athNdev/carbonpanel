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
