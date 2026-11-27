// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const detail = readFileSync(new URL('./[id]/+page.svelte', import.meta.url), 'utf8');
const component = readFileSync(
	new URL('../../lib/components/server-ui/ServerWorld.svelte', import.meta.url),
	'utf8'
);
const worldState = readFileSync(
	new URL('../../lib/features/server-world/state.ts', import.meta.url),
	'utf8'
);

test('server detail exposes a World tab using CarbonTabs and ServerTabPanel', () => {
	assert.match(detail, /import ServerWorld from/);
	assert.match(detail, /\{ id: 'world', label: 'World', icon: Globe2 \}/);
	assert.match(detail, /activeTab === 'world'/);
	assert.match(
		detail,
		/<ServerWorld \{server\} active=\{activeTab === 'world'\} onopenmap=\{openMap\} \/>/
	);
});

test('world tab shows honest management, binding and configuration facts', () => {
	assert.match(component, /describeServerWorld/);
	assert.match(worldState, /MANAGEMENT_PRESENTATION/);
	assert.match(worldState, /'unmanaged'/);
	assert.match(worldState, /'unbound'/);
	assert.match(component, /'Configured level' : 'Active level'/);
	assert.match(component, /Binding & revision/);
	assert.match(component, /relevantWorldConfig\(configCategories\)/);
	assert.doesNotMatch(component, /No active binding|WORLD VIEW OBSERVED/);
});

test('world tab opens the server map dialog with an opaque world key', () => {
	assert.match(component, /encodeWorldKey/);
	assert.match(component, /WorldRef_Kind\.LIBRARY_BINDING/);
	assert.match(component, /WorldRef_Kind\.SERVER_LOCAL/);
	assert.match(component, /onopenmap\(activeMapKey\)/);
	assert.match(detail, /url.searchParams.set\('map', worldKey\)/);
	assert.match(detail, /presentation="dialog"/);
	assert.match(component, /resolve\('\/library'\)/);
	assert.doesNotMatch(component, /href="\/(?!\/)/);
});

test('world tab gates adoption on permission and a read-only compatibility verdict', () => {
	assert.match(component, /checkPermission\(auth\.permissions, 'worlds', 'read', server\.id\)/);
	assert.match(component, /checkPermission\(auth\.permissions, 'worlds', 'update', server\.id\)/);
	assert.match(component, /checkCompatibility/);
	assert.match(component, /adoptionGateReason/);
	assert.match(component, /verdictGatesBinding\(report\?\.overall\)/);
	assert.match(component, /unknownFindings\.length > 0/);
	assert.doesNotMatch(component, /isUnknownConfidence\(report\?\.overall\)/);
});

test('world tab requires explicit confirmation and journals uncertain outcomes without retry', () => {
	assert.match(component, /<CarbonConfirm/);
	assert.match(component, /confirmSwap/);
	assert.match(component, /bindWorld/);
	assert.match(component, /newWorldRequestId/);
	assert.match(component, /bindingOutcomeDisposition/);
	assert.match(component, /operationOutcomePresentation/);
	assert.match(component, /pendingOperation/);
	assert.match(component, /resolveOperation\('rollback'\)/);
	assert.match(component, /aria-live="assertive"/);
	assert.match(component, /never overwritten/);
	assert.doesNotMatch(component, /SendCommand|sendCommand/);
});

test('compatibility is approved for one tuple and discarded when that tuple changes', () => {
	assert.match(worldState, /interface CompatibilityTuple/);
	assert.match(worldState, /sameCompatibilityTuple/);
	assert.match(worldState, /shouldAcceptCompatibility/);
	assert.match(component, /approvedTuple/);
	assert.match(component, /currentTuple/);
	assert.match(component, /navigationGeneration/);
	assert.match(component, /compatibilityRequest/);
	assert.match(component, /staleSelection/);
	assert.match(component, /The checked revision, level or target changed/);
	assert.match(component, /Re-check compatibility/);
	assert.match(component, /sameCompatibilityTuple\(approvedTuple, currentTuple\)/);
	assert.match(component, /shouldAcceptCompatibility\(\{/);
});

test('world tab uses the shared tab chrome and minimizes horizontal overflow', () => {
	assert.match(component, /<ServerTabPanel/);
	assert.match(component, /mode="form"/);
	assert.match(component, /\[&>\*\]:min-w-0/);
	assert.match(component, /break-all/);
});
