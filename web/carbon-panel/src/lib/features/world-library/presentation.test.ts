// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
	adoptionGateReason,
	bindingGateReason,
	bindingOutcomeDisposition,
	classificationPresentation,
	compatibilityVerdictPresentation,
	confidencePresentation,
	consistencyPresentation,
	formatBytes,
	formatTimestamp,
	importPhasePresentation,
	isRestartRequiredClassification,
	isUnknownConfidence,
	isUnsafeClassification,
	modLoaderLabel,
	operationOutcomePresentation,
	shortId,
	transformStatusPresentation,
	verdictGatesBinding
} from './presentation';
import { ModLoader } from '../../proto/carbonpanel/v1/common_pb';

test('confidence is labelled and unknown is the only gating value', () => {
	assert.equal(confidencePresentation('detected').label, 'DETECTED');
	assert.equal(confidencePresentation('detected').tone, 'green');
	assert.equal(confidencePresentation('inferred').label, 'INFERRED');
	assert.equal(confidencePresentation('inferred').tone, 'yellow');
	assert.equal(confidencePresentation('unknown').label, 'UNKNOWN');
	assert.equal(confidencePresentation(undefined).tone, 'gray');
	assert.equal(isUnknownConfidence('detected'), false);
	assert.equal(isUnknownConfidence('inferred'), false);
	assert.equal(isUnknownConfidence('unknown'), true);
	assert.equal(isUnknownConfidence(undefined), true);
	assert.match(confidencePresentation('unknown').hint ?? '', /blocked/);
});

test('status mappings always carry a text label alongside the tone', () => {
	const presentations = [
		importPhasePresentation('complete'),
		importPhasePresentation('failed'),
		operationOutcomePresentation('unknown'),
		compatibilityVerdictPresentation('incompatible'),
		transformStatusPresentation('unsupported'),
		consistencyPresentation('crash-consistent')
	];
	for (const presentation of presentations) {
		assert.ok(presentation.label.length > 0);
		assert.ok(
			['blue', 'green', 'red', 'yellow', 'gray', 'warm-gray', 'purple', 'cyan', 'teal'].includes(
				presentation.tone
			)
		);
	}
	assert.equal(operationOutcomePresentation('unknown').label, 'OUTCOME UNKNOWN');
	assert.match(operationOutcomePresentation('unknown').hint ?? '', /not retried automatically/);
});

test('compatibility verdicts are classified by verdict, not by confidence', () => {
	const expected: Record<string, { label: string; tone: string; gates: boolean }> = {
		compatible: { label: 'COMPATIBLE', tone: 'green', gates: false },
		blocked: { label: 'BLOCKED', tone: 'red', gates: true },
		incompatible: { label: 'INCOMPATIBLE', tone: 'red', gates: true },
		requires_decision: { label: 'REQUIRES DECISION', tone: 'yellow', gates: true },
		unknown: { label: 'UNKNOWN', tone: 'gray', gates: true }
	};
	for (const [verdict, want] of Object.entries(expected)) {
		const presentation = compatibilityVerdictPresentation(verdict);
		assert.equal(presentation.label, want.label, verdict);
		assert.equal(presentation.tone, want.tone, verdict);
		assert.equal(verdictGatesBinding(verdict), want.gates, verdict);
	}
	assert.equal(compatibilityVerdictPresentation(undefined).label, 'UNKNOWN');
	assert.equal(verdictGatesBinding(undefined), true);
	// A real backend verdict must never be mistaken for a confidence value.
	assert.equal(isUnknownConfidence('compatible'), true);
	assert.equal(verdictGatesBinding('compatible'), false);
});

test('binding gate reason follows the verdict and still fails closed on unknown findings', () => {
	assert.equal(bindingGateReason('compatible', 0), null);
	assert.match(bindingGateReason('compatible', 1) ?? '', /Unknown findings gate binding/);
	assert.match(bindingGateReason('blocked', 0) ?? '', /blocked/i);
	assert.match(bindingGateReason('incompatible', 0) ?? '', /blocked/i);
	assert.match(bindingGateReason('requires_decision', 0) ?? '', /operator decision/i);
	assert.match(bindingGateReason('unknown', 0) ?? '', /Unknown findings gate binding/);
	assert.match(bindingGateReason(undefined, 0) ?? '', /Unknown findings gate binding/);
});

test('binding outcomes keep honest labels and dispositions, never generic success', () => {
	const expected: Record<string, { label: string; disposition: string }> = {
		success: { label: 'SUCCESS', disposition: 'applied' },
		resolved: { label: 'RESOLVED', disposition: 'applied' },
		unknown: { label: 'OUTCOME UNKNOWN', disposition: 'uncertain' },
		failed: { label: 'FAILED', disposition: 'failed' },
		queued: { label: 'QUEUED', disposition: 'in_progress' },
		prepared: { label: 'PREPARED', disposition: 'in_progress' },
		running: { label: 'RUNNING', disposition: 'in_progress' },
		skipped: { label: 'SKIPPED', disposition: 'not_applied' },
		cancelled: { label: 'CANCELLED', disposition: 'not_applied' },
		rolled_back: { label: 'ROLLED BACK', disposition: 'not_applied' }
	};
	for (const [outcome, want] of Object.entries(expected)) {
		const presentation = operationOutcomePresentation(outcome);
		assert.equal(presentation.label, want.label, outcome);
		assert.equal(bindingOutcomeDisposition(outcome), want.disposition, outcome);
		assert.ok(presentation.hint && presentation.hint.length > 0, outcome);
	}
	assert.equal(bindingOutcomeDisposition('something-new'), 'unknown');
	assert.match(operationOutcomePresentation('queued').hint ?? '', /not started/);
	assert.match(operationOutcomePresentation('skipped').hint ?? '', /No mutation/);
	assert.match(operationOutcomePresentation('failed').hint ?? '', /No applied state/);
});

test('server-world adoption reuses the compatibility gate and fails closed without a report', () => {
	assert.match(adoptionGateReason(undefined, 0, false), /read-only compatibility check/);
	assert.match(adoptionGateReason('compatible', 0, false), /read-only compatibility check/);
	assert.equal(
		adoptionGateReason('compatible', 0, true),
		'This revision is compatible with the target. Review the confirmation before swapping.'
	);
	assert.match(adoptionGateReason('compatible', 1, true), /Unknown findings gate binding/);
	assert.match(adoptionGateReason('blocked', 0, true), /blocked/i);
	assert.match(adoptionGateReason(undefined, 0, true), /Unknown findings gate binding/);
});

test('classification badges explain when a control applies', () => {
	assert.equal(classificationPresentation('creation_only').label, 'CREATION ONLY');
	assert.equal(classificationPresentation('offline_only').tone, 'cyan');
	assert.equal(classificationPresentation('restart_required').label, 'RESTART REQUIRED');
	assert.equal(isRestartRequiredClassification('restart_required'), true);
	assert.equal(classificationPresentation('unsafe').tone, 'red');
	assert.equal(isUnsafeClassification('unsafe'), true);
	assert.equal(isUnsafeClassification('offline_only'), false);
	assert.match(classificationPresentation('unsafe').hint ?? '', /Disabled/);
});

test('formatting helpers never invent missing values', () => {
	assert.equal(modLoaderLabel(ModLoader.FABRIC), 'fabric');
	assert.equal(modLoaderLabel(undefined), 'UNSPECIFIED');
	assert.equal(formatBytes(undefined), '—');
	assert.equal(formatBytes(0n), '—');
	assert.equal(formatBytes(2048n), '2 KiB');
	assert.equal(shortId('wld_1234567890abcdef'), 'wld_12345678…cdef');
	assert.equal(shortId(undefined), '—');
	assert.equal(formatTimestamp(undefined), '—');
	assert.equal(formatTimestamp({ seconds: 0n }), '—');
	assert.equal(formatTimestamp({ seconds: 1n }), '1970-01-01 00:00 UTC');
});
