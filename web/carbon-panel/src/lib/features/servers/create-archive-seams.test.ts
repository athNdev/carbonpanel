// SPDX-License-Identifier: MPL-2.0
// Seam-level guards for the operator flows. These assert the wiring in the
// actual route/container source, complementing the pure and rendered tests.
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const read = (relative: string) =>
	readFileSync(resolve(import.meta.dirname, '../../..', relative), 'utf8');

const wizard = read('routes/servers/new/+page.svelte');
const detail = read('routes/servers/[id]/+page.svelte');
const container = read('lib/features/servers/ServerArchiveExport.svelte');

test('the wizard submits the completed upload with the stable submission id', () => {
	assert.ok(
		wizard.includes("archiveUploadId: submission ? submission.uploadId : ''"),
		'create must carry the completed archive upload'
	);
	assert.ok(
		wizard.includes('createRequest.requestId = submission.requestId'),
		'archive retries must reuse the same request id captured for the submission'
	);
	assert.ok(
		wizard.includes('previewServerArchive(request, ARCHIVE_RPC_OPTIONS)'),
		'preview must use the extended archive deadline'
	);
	assert.ok(
		wizard.includes('const key = `${uploadId}:${nodeKey}`'),
		'preview responses must be fenced by upload and destination'
	);
});

test('the wizard never stops a live server in the archive flow', () => {
	assert.ok(!wizard.includes('stopServer('), 'the create wizard must not stop any server');
	assert.ok(
		wizard.includes('No running server is stopped by this flow.'),
		'the UI must state that no live server is stopped'
	);
});

test('archive create needs the current successful preview and keeps source identity separate', () => {
	assert.ok(
		wizard.includes('isArchivePreviewCurrent(archiveImportState, archiveNodeId)'),
		'create must require a successful preview of the current upload and node'
	);
	assert.ok(
		wizard.includes('if (!archivePreviewReady)'),
		'the failed-preview state must block create before the request is built'
	);
	assert.ok(
		wizard.includes('archiveImport.retry(archiveNodeId)'),
		'the failed preview must be retryable for the same upload and node'
	);
	assert.match(
		wizard,
		/mcVersion: archivePreview[\s\S]*archivePreview\.mcVersion/,
		'archive create must use the verified Minecraft version'
	);
	assert.match(
		wizard,
		/modLoader: archivePreview \? archivePreview\.modLoader/,
		'archive create must use the verified loader'
	);
	assert.match(
		wizard,
		/loaderVersion: archivePreview \? archivePreview\.loaderVersion/,
		'archive create must use the verified loader version'
	);
	assert.match(
		wizard,
		/memory: formData\.memory,\s*maxPlayers: formData\.maxPlayers,/,
		'operator memory and max-player overrides must survive archive create'
	);
	assert.ok(
		wizard.includes("dockerImage: submission ? '' : formData.dockerImage"),
		'a former blueprint or manual image must not be submitted with an archive'
	);
	assert.ok(
		wizard.includes('detached: submission ? false : formData.detached'),
		'a stale detached flag from an earlier mode must not reach the archive request'
	);
	assert.ok(
		wizard.includes('dockerOverrides: submission ? undefined : formData.dockerOverrides'),
		'stale Docker overrides from an earlier mode must not reach the archive request'
	);
	assert.ok(
		wizard.includes("modpackId: submission ? '' : selectedModpack?.id || ''"),
		'a stale modpack selection must not reach the archive request'
	);
	assert.match(
		wizard,
		/function chooseArchiveMode\(\)[\s\S]*clearBlueprint\(\)/,
		'entering archive mode must clear the former blueprint image'
	);
});

test('archive create binds a concrete destination and refuses generic placement', () => {
	assert.match(
		wizard,
		/const archiveNodeId = \$derived\(\s*archiveMode \? selectedNodeId :/,
		'archive preview and create must use the selected node, not a generic destination'
	);
	assert.ok(
		wizard.includes('Inspection-only preview') || wizard.includes('inspection-only preview'),
		'upload inspection must remain available before a node is chosen'
	);
	assert.match(
		wizard,
		/nodeId: submission\s*\?\s*submission\.nodeId/,
		'archive create must submit exactly the previewed node'
	);
	assert.match(
		wizard,
		/placementStrategy: submission \? ''/,
		'archive create must not ask the controller to place the import automatically'
	);
	assert.match(
		wizard,
		/function ensureArchiveDestination\(\)[\s\S]*nodePlacementMode = 'manual'/,
		'entering archive mode must choose a concrete destination'
	);
});

test('a create-only operator receives a narrow destination preflight', () => {
	assert.ok(
		wizard.includes('archiveImportState.destinations'),
		'archive mode must consume the preview destination list when ListNodes is denied'
	);
	assert.ok(
		wizard.includes('data-testid="archive-destination-option"'),
		'the narrow destination list must be selectable'
	);
	assert.ok(
		wizard.includes('archive-create-blocked-help'),
		'the final step must explain what still blocks create'
	);
	assert.ok(
		wizard.includes('archiveCreateReady'),
		'create must only enable once the current archive and destination are ready'
	);
});

test('the current archive preflight outranks cached node inventory', () => {
	assert.match(
		wizard,
		/const archivePreflightSettled = \$derived\([\s\S]*preflightUploadId === archiveImportState\.uploadId/,
		'a destination preflight is authoritative only for its own upload'
	);
	assert.match(
		wizard,
		/const destinationChoices = \$derived\.by\(\(\) => \{[\s\S]*if \(archivePreflightSettled\) \{[\s\S]*archiveImportState\.destinations\.map[\s\S]*\n\t\t\}\n\t\treturn \[\];\n\t\}\);/,
		'a settled preflight replaces cached inventory while it is being refreshed'
	);
	assert.match(
		wizard,
		/if \(!archivePreflightSettled\) return;\s*if \(archiveImportState\.destinations\.some\(\(destination\) => destination\.id === selectedNodeId\)\)\s*return;/,
		'a still-eligible selection is preserved, otherwise it is replaced'
	);
	assert.ok(
		wizard.includes('data-testid="archive-destination-empty"'),
		'a settled preflight with zero enabled nodes must render an empty state'
	);
	assert.ok(
		wizard.includes('data-testid="archive-destination-retry"'),
		'the empty state must offer an actionable preflight retry'
	);
	assert.ok(
		wizard.includes("void archiveImport.retry('')"),
		'the empty state must re-run the inspection so newly enabled nodes appear'
	);
});

test('archive mode hides fields the controller rejects', () => {
	assert.ok(
		wizard.includes('data-testid="archive-runtime-fields-locked"'),
		'archive mode must state that image, detached mode and overrides are managed'
	);
	assert.match(
		wizard,
		/<!-- Detached Mode -->\s*\{#if !archiveMode\}/,
		'detached mode must not be offered for an archive'
	);
	assert.match(
		wizard,
		/\{#if !archiveMode\}\s*<div class="border-t border-border pt-2">\s*<DockerOverridesEditor/,
		'Docker overrides must not be offered for an archive'
	);
});

test('an in-flight archive create is captured and navigation is fenced', () => {
	assert.ok(
		wizard.includes('interface ArchiveSubmission'),
		'the submission must carry its upload, node, request id and preview'
	);
	assert.ok(
		wizard.includes('archiveSubmission = submission'),
		'create must capture the submission before awaiting the controller'
	);
	assert.ok(
		wizard.includes('if (archiveSubmission) cancel();'),
		'browser navigation must be cancelled while the archive create is in flight'
	);
	assert.ok(
		wizard.includes('disabled={Boolean(archiveSubmission)}'),
		'upload, mode and destination controls must be disabled during the submission'
	);
	assert.ok(
		wizard.includes('archiveImportState.uploadId === submission.uploadId'),
		'completion must only release the captured upload identity'
	);
	assert.match(
		wizard,
		/archiveSubmission = null;\s*createdServer = response\.server;/,
		'a committed create must clear the in-flight fence before navigating'
	);
});

test('leaving archive mode restores the ordinary placement state', () => {
	assert.ok(wizard.includes('interface ArchivePlacementSnapshot'));
	assert.match(
		wizard,
		/function leaveArchiveMode\(\)[\s\S]*nodePlacementMode = priorPlacement\.nodePlacementMode[\s\S]*selectedNodeId = priorPlacement\.selectedNodeId[\s\S]*placementStrategy = priorPlacement\.placementStrategy/,
		'the prior auto/manual mode, node and strategy must come back'
	);
	assert.match(
		wizard,
		/function applyBlueprint\(seed: ServerBlueprint\) \{[\s\S]*leaveArchiveMode\(\)/,
		'a blueprint switch must leave archive mode through the restoring path'
	);
	assert.match(
		wizard,
		/async function selectModpack\(modpack: IndexedModpack\) \{[\s\S]*leaveArchiveMode\(\)/,
		'a modpack switch must leave archive mode through the restoring path'
	);
});

test('archive mode locks the runtime and disables standalone add-ons', () => {
	assert.ok(
		wizard.includes('data-testid="archive-runtime-mc"'),
		'the archive runtime must be visible in the engine step'
	);
	assert.ok(
		wizard.includes('data-testid="archive-addons-disabled"'),
		'archive mode must explain that standalone add-ons are disabled'
	);
	assert.match(
		wizard,
		/function buildSetupTasks\(\): SetupTask\[\] \{[\s\S]*if \(archiveMode\) return \[\];/,
		'archive create must not run standalone setup tasks'
	);
	assert.match(
		wizard,
		/function clearStandaloneAddons\(\)[\s\S]*selectedMods = \[\][\s\S]*selectedDatapacks = \[\][\s\S]*selectedModules = \[\]/,
		'entering archive mode must clear former standalone add-on selections'
	);
});

test('a replacement upload fences an older completion and blocks submission', () => {
	assert.match(
		wizard,
		/let archiveUploadSequence = 0/,
		'uploads need a generation so a superseded completion cannot win'
	);
	assert.match(
		wizard,
		/const generation = \+\+archiveUploadSequence/,
		'each archive upload must take a new generation'
	);
	assert.match(
		wizard,
		/if \(generation !== archiveUploadSequence\) return;/,
		'a late upload completion must not replace a newer archive'
	);
	assert.ok(
		wizard.includes('archiveUploading ||'),
		'create must be blocked while a replacement upload is in flight'
	);
	assert.ok(
		wizard.includes('Wait for the replacement archive upload to finish before creating.'),
		'the operator must be told why create is blocked during a replacement upload'
	);
});

test('export results are fenced to the current modal attempt and server', () => {
	assert.match(container, /let attempt = 0;/, 'preparations need a generation');
	assert.match(
		container,
		/const generation = \+\+attempt;/,
		'each preparation must take a new generation'
	);
	assert.ok(
		container.includes('generation !== attempt || !open || server.id !== requestedServerId'),
		'a late success or rejection must not touch a reopened dialog or another server'
	);
	assert.match(
		container,
		/const key = `\$\{server\.id\}:\$\{server\.status\}`;[\s\S]*reset\(\);/,
		'a server identity or status change must release the former attempt'
	);
	assert.ok(
		container.includes('onDestroy(invalidate)') &&
			container.includes(
				"if (!acknowledged || !sessionId || !filename || phase !== 'ready') return;"
			),
		'teardown must fence late work and download must require the current ready session'
	);
});

test('archive cards keep selected descriptions and the same-node explanation readable', () => {
	assert.match(
		wizard,
		/data-testid="create-from-archive-tile"[\s\S]*archiveMode\s*\? 'text-accent-foreground'/,
		'the selected archive card description must use the accent foreground token'
	);
	assert.doesNotMatch(
		wizard,
		/(?<!aria-)disabled=\{archiveMode\}/,
		'the archive-disabled Auto-Placement radio must stay focusable through aria-disabled'
	);
	assert.match(
		wizard,
		/aria-disabled=\{archiveMode\}[\s\S]*?class="mt-1 text-xs \{archiveMode\s*\? 'text-foreground'/,
		'the essential same-node explanation must not inherit the disabled opacity'
	);
});

test('the operator export control is mounted on server detail', () => {
	assert.ok(detail.includes('ServerArchiveExport'), 'server detail must expose the export control');
	assert.ok(
		container.includes('triggerStreamDownload(sessionId, filename)'),
		'download must reuse the authorized stream transport'
	);
	assert.ok(
		container.includes('consistency: STOPPED_CONSISTENCY'),
		'export must request stopped-server consistency'
	);
	assert.ok(
		container.includes('ARCHIVE_CREDENTIAL_DISCLOSURE'),
		'the disclosure must be passed into the export panel'
	);
	assert.ok(
		!container.includes('stopServer('),
		'export must refuse a live server rather than stopping it'
	);
});
