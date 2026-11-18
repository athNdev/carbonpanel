// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
	bindingGateReason,
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
