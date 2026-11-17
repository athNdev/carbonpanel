// SPDX-License-Identifier: MPL-2.0
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

function route(segment: string): string {
	return readFileSync(new URL(`../../../routes/library/${segment}`, import.meta.url), 'utf8');
}

const list = route('+page.svelte');
const detail = route('[id]/+page.svelte');
const importPage = route('import/+page.svelte');
const wizard = route('new/+page.svelte');

for (const [name, source] of [
	['list', list],
	['detail', detail],
	['import', importPage],
	['wizard', wizard]
] as const) {
	test(`${name} route follows the page-header and token contract`, () => {
		const markup = source.slice(source.indexOf('</script>'));
		const firstCarbon = markup.indexOf('<Carbon');
		assert.ok(firstCarbon >= 0, `${name} must render a Carbon primitive`);
		assert.match(
			markup.slice(firstCarbon),
			/^<CarbonPageHeader/,
			`${name} must start with CarbonPageHeader`
		);
		assert.doesNotMatch(source, /class="[^"]*\bp-[68]\b/, `${name} must not add a page gutter`);
		assert.doesNotMatch(
			source,
			/#[0-9a-fA-F]{3,8}\b/,
			`${name} must not use arbitrary hex colours`
		);
		assert.match(
			source,
			/border-border|bg-card|text-foreground/,
			`${name} must use semantic tokens`
		);
		assert.match(source, /resolve\(/, `${name} must navigate through resolve()`);
		assert.doesNotMatch(source, /href="\/(?!\/)/, `${name} must not hard-code internal hrefs`);
		assert.match(
			source,
			/focus-ring/,
			`${name} must expose a visible focus ring on interactive links`
		);
	});
}

test('library list has grouped controls and distinct loading, empty, no-match and error states', () => {
	assert.match(list, /<CarbonSearch/);
	assert.match(list, /<CarbonSelect[\s\S]*Edition/);
	assert.match(list, /<CarbonSelect[\s\S]*Sort by/);
	assert.match(list, /role="alert"|CarbonErrorState/);
	assert.match(list, /No worlds in the library yet/);
	assert.match(list, /No matching worlds/);
	assert.match(list, /aria-label=\{?["']Loading world library/);
	assert.match(list, /\{#each filtered as entry \(entry\.id\)\}/);
	assert.match(list, /<CarbonDataTable/);
});

test('detail route shows classified findings, gating, binding and explicit recovery', () => {
	assert.match(detail, /checkCompatibility/);
	assert.match(detail, /confidencePresentation/);
	assert.match(detail, /Unknown findings gate binding/);
	assert.match(detail, /gatedByUnknown/);
	assert.match(detail, /operationOutcomePresentation\(pendingOperation\.outcome\)/);
	assert.match(detail, /resolveOperation/);
	assert.match(detail, /No silent retry|not retried automatically|resolve it before trying again/i);
	assert.match(detail, /aria-live="assertive"/);
	assert.match(detail, /unbindWorld/);
	assert.match(detail, /consistencyPresentation/);
	assert.doesNotMatch(detail, /resolveWorldOperation[\s\S]{0,80}catch[\s\S]{0,80}bindWorld/);
});

test('import route separates artifact import from manual prebuilt-world import', () => {
	assert.match(importPage, /searchWorldArtifacts/);
	assert.match(importPage, /getProviderCapabilities/);
	assert.match(importPage, /A modpack or datapack is not a prebuilt world/);
	assert.match(importPage, /No automated prebuilt-world discovery/);
	assert.match(importPage, /D11/);
	assert.match(importPage, /Planet Minecraft/);
	assert.match(importPage, /MinecraftMaps/);
	assert.match(importPage, /Marketplace/);
	assert.match(importPage, /projectTypes = \[[^\]]*modpack[^\]]*datapack/);
	assert.match(importPage, /<CarbonEmptyState/);
});

test('wizard is grouped, schema-derived and previews before creating', () => {
	for (const label of ['Basic', 'Worldgen', 'Gameplay', 'Datapacks', 'Advanced / Expert']) {
		assert.ok(wizard.includes(label), `missing wizard group ${label}`);
	}
	assert.match(wizard, /getWorldConfigSchema/);
	assert.match(wizard, /control\.group/);
	assert.match(wizard, /stepFor\(control\)/);
	assert.match(wizard, /classificationPresentation/);
	assert.match(wizard, /isUnsafeClassification/);
	assert.match(wizard, /applies: \{control\.appliesWhen/i);
	assert.match(wizard, /previewWorldConfig/);
	assert.match(wizard, /createWorld\(/);
	assert.match(wizard, /preview\.validationErrors/);
	assert.match(wizard, /preview\.conflicts/);
	assert.ok(
		wizard.indexOf('previewWorldConfig') < wizard.indexOf('createWorld('),
		'preview must precede creation'
	);
	assert.match(wizard, /<CarbonAccordionItem/);
});

test('production store composes every required RPC through create(Schema, ...)', () => {
	const store = readFileSync(new URL('../../stores/world-library.ts', import.meta.url), 'utf8');
	assert.match(store, /rpcClient\.world/);
	for (const method of [
		'listWorldEntries',
		'getWorldEntry',
		'listRevisions',
		'listBindings',
		'listImportJobs',
		'importWorld',
		'checkCompatibility',
		'bindWorld',
		'unbindWorld',
		'resolveWorldOperation',
		'getWorldConfigSchema',
		'previewWorldConfig',
		'createWorldFromConfig',
		'searchWorldArtifacts',
		'getProviderCapabilities'
	]) {
		assert.ok(store.includes(method), `store is missing ${method}`);
	}
	assert.match(store, /create\(ListWorldEntriesRequestSchema/);
	assert.match(store, /create\(CreateWorldFromConfigRequestSchema/);
	assert.match(store, /silentCallOptions/);
});
