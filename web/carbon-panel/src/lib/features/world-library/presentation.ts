// SPDX-License-Identifier: MPL-2.0
import { ModLoader } from '../../proto/carbonpanel/v1/common_pb';

/**
 * Tone names are the CarbonTag `type` values. A label always accompanies the
 * tone in the views: colour is never the only explanation of a state.
 */
export type WorldTagTone =
	'blue' | 'green' | 'red' | 'yellow' | 'gray' | 'warm-gray' | 'purple' | 'cyan' | 'teal';

export interface WorldPresentation {
	label: string;
	tone: WorldTagTone;
	/** Short, plain-language explanation shown next to the tag where it matters. */
	hint?: string;
}

function upper(value: string): string {
	return value.replace(/[_-]+/g, ' ').trim().toUpperCase() || 'UNKNOWN';
}

/** Detected / inferred / unknown provenance. Unknown always gates the action. */
export function confidencePresentation(confidence: string | undefined): WorldPresentation {
	switch (confidence) {
		case 'detected':
			return { label: 'DETECTED', tone: 'green', hint: 'Read from the world itself' };
		case 'inferred':
			return { label: 'INFERRED', tone: 'yellow', hint: 'Derived from surrounding evidence' };
		case 'unknown':
		default:
			return {
				label: 'UNKNOWN',
				tone: 'gray',
				hint: 'Not verifiable for this target; the action is blocked'
			};
	}
}

export function isUnknownConfidence(confidence: string | undefined): boolean {
	return confidencePresentation(confidence).label === 'UNKNOWN';
}

/** Durability phases of an import job. */
export function importPhasePresentation(phase: string | undefined): WorldPresentation {
	switch (phase) {
		case 'complete':
			return { label: 'COMPLETE', tone: 'green' };
		case 'downloading':
			return { label: 'DOWNLOADING', tone: 'blue' };
		case 'verifying':
			return { label: 'VERIFYING', tone: 'cyan' };
		case 'failed':
			return { label: 'FAILED', tone: 'red' };
		case 'cancelled':
			return { label: 'CANCELLED', tone: 'gray' };
		case 'pending':
		default:
			return { label: phase ? upper(phase) : 'PENDING', tone: 'yellow' };
	}
}

/**
 * Bind/unbind journal outcomes, including the uncertain one. Every reported
 * outcome keeps its own label and explanation: a queued, skipped, cancelled or
 * failed operation must never be dressed up as an applied success.
 */
export function operationOutcomePresentation(outcome: string | undefined): WorldPresentation {
	switch (outcome) {
		case 'applied':
		case 'complete':
		case 'committed':
			return { label: 'APPLIED', tone: 'green', hint: 'The backend confirmed the change' };
		case 'success':
			return {
				label: 'SUCCESS',
				tone: 'green',
				hint: 'The backend confirmed the change and recorded it in the journal.'
			};
		case 'unknown':
		case 'uncertain':
			return {
				label: 'OUTCOME UNKNOWN',
				tone: 'yellow',
				hint: 'The mutation may have happened. Resolve it from real node state; it is not retried automatically.'
			};
		case 'resolved':
			return {
				label: 'RESOLVED',
				tone: 'green',
				hint: 'The operator decision was recorded in the journal.'
			};
		case 'rolled_back':
		case 'rollback':
			return {
				label: 'ROLLED BACK',
				tone: 'gray',
				hint: 'The prior level family was restored; no new world was applied.'
			};
		case 'failed':
			return {
				label: 'FAILED',
				tone: 'red',
				hint: 'The operation reported a definite failure. No applied state is claimed.'
			};
		case 'queued':
		case 'pending':
		case 'prepared':
			return {
				label: outcome ? upper(outcome) : 'QUEUED',
				tone: 'blue',
				hint: 'The intent was recorded but the attempt has not started; no world bytes were changed.'
			};
		case 'running':
		case 'in_progress':
			return {
				label: outcome ? upper(outcome) : 'RUNNING',
				tone: 'blue',
				hint: 'The operation is still being attempted. Wait for completion; no success is claimed yet.'
			};
		case 'skipped':
			return {
				label: 'SKIPPED',
				tone: 'warm-gray',
				hint: 'No mutation was performed for this target.'
			};
		case 'cancelled':
		case 'canceled':
			return {
				label: 'CANCELLED',
				tone: 'gray',
				hint: 'The operation was cancelled before mutating world bytes.'
			};
		default:
			return {
				label: outcome ? upper(outcome) : 'UNKNOWN',
				tone: 'gray',
				hint: 'The backend reported an unrecognised outcome. Check the journal before claiming success.'
			};
	}
}

export type BindingOutcomeDisposition =
	'applied' | 'uncertain' | 'in_progress' | 'failed' | 'not_applied' | 'unknown';

/** How the UI may react to a reported bind outcome. Never auto-retries. */
export function bindingOutcomeDisposition(outcome: string | undefined): BindingOutcomeDisposition {
	switch (outcome) {
		case 'applied':
		case 'complete':
		case 'committed':
		case 'success':
		case 'resolved':
			return 'applied';
		case 'unknown':
		case 'uncertain':
			return 'uncertain';
		case 'queued':
		case 'pending':
		case 'prepared':
		case 'running':
		case 'in_progress':
			return 'in_progress';
		case 'failed':
			return 'failed';
		case 'skipped':
		case 'cancelled':
		case 'canceled':
		case 'rolled_back':
		case 'rollback':
			return 'not_applied';
		default:
			return 'unknown';
	}
}

/** Overall read-only compatibility verdict. */
export function compatibilityVerdictPresentation(overall: string | undefined): WorldPresentation {
	switch (overall) {
		case 'compatible':
			return { label: 'COMPATIBLE', tone: 'green', hint: 'No detected blocker for this target' };
		case 'blocked':
			return {
				label: 'BLOCKED',
				tone: 'red',
				hint: 'A required transform is unsupported; binding is disabled'
			};
		case 'incompatible':
			return { label: 'INCOMPATIBLE', tone: 'red', hint: 'Binding is disabled' };
		case 'requires_decision':
			return {
				label: 'REQUIRES DECISION',
				tone: 'yellow',
				hint: 'An operator must review the report before binding'
			};
		case 'unknown':
		default:
			return {
				label: overall ? upper(overall) : 'UNKNOWN',
				tone: 'gray',
				hint: 'Unknown findings gate the operation'
			};
	}
}

/**
 * Verdicts are not confidence values. `compatible` is the only verdict that
 * permits binding; every other real verdict (and a missing verdict) fails
 * closed. Unknown individual findings also gate even when the overall verdict
 * claims compatibility, so an inconsistent report cannot silently unlock a
 * destructive operation.
 */
export function verdictGatesBinding(overall: string | undefined): boolean {
	switch (overall) {
		case 'compatible':
			return false;
		default:
			return true;
	}
}

/** The operator-facing reason binding is unavailable, or null when it is not. */
export function bindingGateReason(
	overall: string | undefined,
	unknownFindingCount: number
): string | null {
	if (unknownFindingCount > 0 || overall === 'unknown' || overall === undefined) {
		return 'Unknown findings gate binding. The check is read-only; resolve the missing evidence before continuing.';
	}
	switch (overall) {
		case 'compatible':
			return null;
		case 'blocked':
		case 'incompatible':
			return 'This change is blocked for the target. Binding is disabled; choose a supported revision or transform.';
		case 'requires_decision':
			return 'This change requires an explicit operator decision. Review the report before binding.';
		default:
			return 'Unknown findings gate binding. The check is read-only; resolve the missing evidence before continuing.';
	}
}

/**
 * Gate reason for adopting a library revision onto a server level from the
 * server-detail WORLD tab. It reuses the library detail verdict/confidence
 * policy: compatible with no unknown findings is the only state that permits
 * the mutating bind. Missing evidence still fails closed.
 */
export function adoptionGateReason(
	overall: string | undefined,
	unknownFindingCount: number,
	reportPresent: boolean
): string {
	if (!reportPresent) {
		return 'Run a read-only compatibility check before swapping this level. Nothing is mutated by the check.';
	}
	return (
		bindingGateReason(overall, unknownFindingCount) ??
		'This revision is compatible with the target. Review the confirmation before swapping.'
	);
}

/** Transform dry-run status. Unsupported/blocked transforms are never applied. */
export function transformStatusPresentation(status: string | undefined): WorldPresentation {
	switch (status) {
		case 'supported':
			return { label: 'SUPPORTED', tone: 'green' };
		case 'requires_decision':
			return { label: 'REQUIRES DECISION', tone: 'yellow' };
		case 'blocked':
			return { label: 'BLOCKED', tone: 'red' };
		case 'unsupported':
		default:
			return {
				label: status ? upper(status) : 'UNSUPPORTED',
				tone: 'warm-gray',
				hint: 'This transform is not available for the target'
			};
	}
}

/**
 * Schema control classification. The badge answers WHEN a control applies;
 * the schema's own `appliesWhen` text is the authoritative, versioned detail.
 */
export function classificationPresentation(classification: string | undefined): WorldPresentation {
	switch (classification) {
		case 'creation_only':
			return { label: 'CREATION ONLY', tone: 'blue' };
		case 'offline_only':
			return { label: 'OFFLINE ONLY', tone: 'cyan' };
		case 'live_command':
			return { label: 'LIVE COMMAND', tone: 'yellow' };
		case 'restart_required':
			return { label: 'RESTART REQUIRED', tone: 'yellow' };
		case 'unsafe':
			return {
				label: 'UNSAFE',
				tone: 'red',
				hint: 'Disabled: this control cannot be applied safely for the selected target'
			};
		default:
			return { label: classification ? upper(classification) : 'GENERAL', tone: 'gray' };
	}
}

export function isUnsafeClassification(classification: string | undefined): boolean {
	return classification === 'unsafe';
}

export function isRestartRequiredClassification(classification: string | undefined): boolean {
	return classification === 'restart_required';
}

/** Binding consistency as reported by the journal, never inferred from a tag colour. */
export function consistencyPresentation(consistency: string | undefined): WorldPresentation {
	switch (consistency) {
		case 'verified stopped':
			return { label: 'VERIFIED STOPPED', tone: 'green' };
		case 'verified quiesced':
			return { label: 'VERIFIED QUIESCED', tone: 'blue' };
		case 'crash-consistent':
			return {
				label: 'CRASH-CONSISTENT',
				tone: 'yellow',
				hint: 'Recovered state; verify the level before relying on it'
			};
		default:
			return { label: consistency ? upper(consistency) : 'UNKNOWN', tone: 'gray' };
	}
}

export function modLoaderLabel(loader: number | undefined): string {
	if (loader === undefined) return 'UNSPECIFIED';
	const name = ModLoader[loader];
	return name ? name.replace(/_/g, ' ').toLowerCase() : 'UNSPECIFIED';
}

export function formatBytes(size: bigint | number | undefined): string {
	const value = typeof size === 'bigint' ? Number(size) : (size ?? 0);
	if (!Number.isFinite(value) || value <= 0) return '—';
	const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
	let amount = value;
	let index = 0;
	while (amount >= 1024 && index < units.length - 1) {
		amount /= 1024;
		index++;
	}
	const text = Number.isInteger(amount) ? String(amount) : amount.toFixed(1);
	return `${text} ${units[index]}`;
}

/** Shorten immutable IDs so tables cannot be forced into horizontal page scroll. */
export function shortId(id: string | undefined): string {
	if (!id) return '—';
	if (id.length <= 16) return id;
	return `${id.slice(0, 12)}…${id.slice(-4)}`;
}

export function formatTimestamp(value: { seconds: bigint } | undefined): string {
	if (!value) return '—';
	const milliseconds = Number(value.seconds) * 1000;
	if (!Number.isFinite(milliseconds) || milliseconds <= 0) return '—';
	return new Date(milliseconds).toISOString().replace('T', ' ').slice(0, 16) + ' UTC';
}
