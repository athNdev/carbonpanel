<!--
SPDX-License-Identifier: MPL-2.0
-->

<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import { toast } from 'svelte-sonner';
	import { newWorldRequestId, worldLibraryActions } from '$lib/stores/world-library';
	import { formatRpcError } from '$lib/api/rpc-error';
	import type { ArtifactSearchResult } from '$lib/features/world-library/actions';
	import type { ProviderCapability, WorldImportJob } from '$lib/proto/carbonpanel/v1/world_pb';
	import {
		importPhasePresentation,
		modLoaderLabel,
		shortId
	} from '$lib/features/world-library/presentation';
	import {
		CarbonBreadcrumbs,
		CarbonButton,
		CarbonEmptyState,
		CarbonPageHeader,
		CarbonSelect,
		CarbonTag,
		CarbonTextInput
	} from '$lib/components/carbon';
	import { AlertTriangle, ExternalLink, Search, Upload } from '@lucide/svelte';

	const projectTypes = ['mod', 'modpack', 'resourcepack', 'shader', 'datapack', 'plugin'];
	const manualProviders = ['Planet Minecraft', 'MinecraftMaps', 'Minecraft Marketplace'];

	let capabilities = $state<ProviderCapability[]>([]);
	let capabilityError = $state<unknown>(null);

	let artifactProvider = $state<'modrinth' | 'curseforge'>('modrinth');
	let artifactProjectType = $state('modpack');
	let artifactQuery = $state('');
	let artifactVersion = $state('');
	let artifactLoader = $state('1');
	let artifactBusy = $state(false);
	let artifactError = $state<unknown>(null);
	let artifacts = $state<ArtifactSearchResult>({
		artifacts: [],
		nextCursor: '',
		rateLimited: false
	});

	let importSource = $state<'url' | 'upload' | 'backup'>('url');
	let importUrl = $state('');
	let uploadId = $state('');
	let displayName = $state('');
	let sourcePlatform = $state('');
	let licenseAttestation = $state('');
	let sha256Checksum = $state('');
	let requestId = $state(newWorldRequestId('import'));
	let importBusy = $state(false);
	let importError = $state<unknown>(null);
	let importJob = $state<WorldImportJob | undefined>(undefined);
	let importedEntryId = $state('');

	onMount(() => {
		void worldLibraryActions
			.getProviderCapabilities()
			.then((outcome) => {
				if (outcome.kind === 'capabilities') capabilities = outcome.value;
			})
			.catch((error) => (capabilityError = error));
	});

	async function searchArtifacts() {
		artifactBusy = true;
		artifactError = null;
		try {
			const outcome = await worldLibraryActions.searchWorldArtifacts({
				provider: artifactProvider,
				query: artifactQuery.trim(),
				mcVersion: artifactVersion.trim(),
				modLoader: Number(artifactLoader),
				projectType: artifactProjectType,
				limit: 20
			});
			if (outcome.kind === 'searched') artifacts = outcome.value;
		} catch (error) {
			artifactError = error;
			artifacts = { artifacts: [], nextCursor: '', rateLimited: false };
		} finally {
			artifactBusy = false;
		}
	}

	async function importWorld() {
		importBusy = true;
		importError = null;
		importedEntryId = '';
		importJob = undefined;
		try {
			const outcome = await worldLibraryActions.importWorld({
				source: importSource,
				url: importSource === 'upload' ? '' : importUrl.trim(),
				uploadId: importSource === 'upload' ? uploadId.trim() : '',
				displayName: displayName.trim(),
				sourcePlatform: sourcePlatform.trim(),
				licenseAttestation: licenseAttestation.trim(),
				sha256Checksum: sha256Checksum.trim(),
				requestId: requestId.trim() || newWorldRequestId('import')
			});
			if (outcome.kind !== 'imported') return;
			importJob = outcome.value.job;
			importedEntryId = importJob?.worldEntryId ?? '';
			requestId = newWorldRequestId('import');
			toast.success('Import job journalled. Verification runs before a revision is published.');
		} catch (error) {
			importError = error;
		} finally {
			importBusy = false;
		}
	}
</script>

<div class="space-y-6 font-sans text-foreground">
	<CarbonPageHeader
		title="Import a world"
		description="Import from a content API, or from a URL or file upload."
	>
		{#snippet tag()}
			<CarbonTag type="blue" size="sm">IMPORT</CarbonTag>
		{/snippet}
		{#snippet actions()}
			<CarbonButton kind="secondary" size="md" href={resolve('/library')} class="justify-center">
				Back to library
			</CarbonButton>
		{/snippet}
	</CarbonPageHeader>

	<CarbonBreadcrumbs
		items={[{ href: resolve('/library'), label: 'World Library' }, { label: 'Import' }]}
	/>

	<section class="space-y-3 border border-border bg-card p-4" aria-labelledby="artifact-heading">
		<div>
			<h2 id="artifact-heading" class="text-base font-semibold text-foreground">
				API-backed artifact search
			</h2>
			<p class="mt-1 text-xs text-muted-foreground">
				Modrinth and CurseForge expose <strong>content artifacts</strong> used by a world workflow (mods,
				modpacks, resource packs, shaders, datapacks, plugins). This is an artifact import, not world
				discovery.
			</p>
		</div>
		<p
			class="flex items-start gap-2 border border-warning bg-background p-3 text-xs text-warning-text"
		>
			<AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />
			<span>
				A modpack or datapack is not a prebuilt world. Search results are project files; they do not
				contain a ready-to-play level.
			</span>
		</p>

		<div class="flex flex-wrap items-end gap-3">
			<div class="w-40">
				<CarbonSelect label="Provider" bind:value={artifactProvider}>
					<option value="modrinth">Modrinth</option>
					<option value="curseforge">CurseForge</option>
				</CarbonSelect>
			</div>
			<div class="w-40">
				<CarbonSelect label="Artifact type" bind:value={artifactProjectType}>
					{#each projectTypes as type (type)}
						<option value={type}>{type}</option>
					{/each}
				</CarbonSelect>
			</div>
			<div class="min-w-48 flex-1">
				<CarbonTextInput
					label="Search query"
					placeholder="Project name or ID"
					bind:value={artifactQuery}
				/>
			</div>
			<div class="w-36">
				<CarbonTextInput
					label="Minecraft version"
					placeholder="1.21.1"
					bind:value={artifactVersion}
				/>
			</div>
			<div class="w-36">
				<CarbonSelect label="Loader" bind:value={artifactLoader}>
					<option value="1">Vanilla</option>
					<option value="2">Forge</option>
					<option value="3">Fabric</option>
					<option value="4">Quilt</option>
					<option value="5">Paper</option>
				</CarbonSelect>
			</div>
			<CarbonButton
				kind="secondary"
				size="sm"
				loading={artifactBusy}
				disabled={!artifactQuery.trim()}
				onclick={() => void searchArtifacts()}
				class="justify-center gap-2"
			>
				<Search class="h-4 w-4" />
				Search artifacts
			</CarbonButton>
		</div>

		{#if artifactError}
			<div
				role="alert"
				class="border border-destructive bg-background p-3 text-xs text-destructive-text"
			>
				{formatRpcError(artifactError, 'Artifact search failed.')}
			</div>
		{/if}
		{#if artifacts.rateLimited}
			<p class="text-xs text-warning-text" role="status">
				The provider is rate limiting requests. Try again later; no results were invented.
			</p>
		{/if}

		{#if artifacts.artifacts.length === 0}
			<CarbonEmptyState
				title="No artifacts searched yet"
				description="Choose a provider and project type, then search. Results come from the provider API."
			/>
		{:else}
			<div class="overflow-x-auto">
				<table class="w-full border-collapse text-left text-sm">
					<thead class="border-b border-border text-xs text-muted-foreground uppercase">
						<tr>
							<th scope="col" class="px-3 py-2">Project</th>
							<th scope="col" class="px-3 py-2">Type</th>
							<th scope="col" class="px-3 py-2">Version / loader</th>
							<th scope="col" class="px-3 py-2">License</th>
							<th scope="col" class="px-3 py-2">Source</th>
						</tr>
					</thead>
					<tbody class="divide-y divide-border">
						{#each artifacts.artifacts as artifact, index (index)}
							<tr class="hover:bg-accent">
								<td class="px-3 py-2.5 text-xs text-foreground">
									<span class="block font-medium">{artifact.name}</span>
									<span class="font-mono text-muted-foreground">
										{artifact.provider} / {artifact.projectId}
									</span>
								</td>
								<td class="px-3 py-2.5">
									<CarbonTag type={artifact.projectType === 'datapack' ? 'teal' : 'blue'} size="sm">
										{artifact.projectType.toUpperCase()}
									</CarbonTag>
								</td>
								<td class="px-3 py-2.5 font-mono text-xs text-muted-foreground">
									{artifact.mcVersion || '—'} · {modLoaderLabel(artifact.modLoader)}
								</td>
								<td class="px-3 py-2.5 text-xs text-muted-foreground">
									{artifact.license || '—'}
								</td>
								<td class="px-3 py-2.5 text-xs">
									{#if artifact.sourceUrl}
										<!-- eslint-disable svelte/no-navigation-without-resolve -- external provider source URL -->
										<a
											href={artifact.sourceUrl}
											target="_blank"
											rel="noopener noreferrer"
											class="focus-ring inline-flex items-center gap-1 text-accent-text hover:underline"
										>
											Source <ExternalLink class="h-3 w-3" />
										</a>
										<!-- eslint-enable svelte/no-navigation-without-resolve -->
									{:else}
										<span class="text-muted-foreground">—</span>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</section>

	<section class="space-y-3 border border-border bg-card p-4" aria-labelledby="manual-heading">
		<div>
			<h2 id="manual-heading" class="text-base font-semibold text-foreground">
				Manual URL / file import
			</h2>
			<p class="mt-1 text-xs text-muted-foreground">
				The supported fallback for a prebuilt world. The archive is staged and verified node-locally
				before an immutable revision is published.
			</p>
		</div>
		<p class="border border-border bg-background p-3 text-xs text-muted-foreground" role="status">
			<strong class="text-foreground">No automated prebuilt-world discovery.</strong>
			{manualProviders.join(', ')} are manual URL/file import only. This is investigation gate D11: there
			is no world-project category to search, so nothing is queried on your behalf.
		</p>

		{#if capabilityError}
			<div
				role="alert"
				class="border border-destructive bg-background p-3 text-xs text-destructive-text"
			>
				Provider capabilities are unavailable: {formatRpcError(capabilityError, 'Unknown error')}
			</div>
		{:else if capabilities.length > 0}
			<div class="grid gap-2 md:grid-cols-2 [&>*]:min-w-0">
				{#each capabilities as capability, index (index)}
					<div class="border border-border bg-background p-3">
						<div class="flex flex-wrap items-center gap-2">
							<span class="text-xs font-semibold text-foreground">{capability.provider}</span>
							<CarbonTag type={capability.apiBackedArtifacts ? 'green' : 'gray'} size="sm">
								{capability.apiBackedArtifacts ? 'API ARTIFACTS' : 'NO API ARTIFACTS'}
							</CarbonTag>
							<CarbonTag type={capability.prebuiltWorldCatalog ? 'blue' : 'gray'} size="sm">
								{capability.prebuiltWorldCatalog ? 'WORLD CATALOG' : 'NO WORLD CATALOG'}
							</CarbonTag>
						</div>
						<p class="mt-2 text-xs text-muted-foreground">
							{capability.note || 'No provider note.'}
						</p>
						<p class="mt-1 font-mono text-xs break-all text-warning-text">
							Gate: {capability.discoveryGate || 'D11'}
						</p>
					</div>
				{/each}
			</div>
		{/if}

		<div class="grid gap-3 md:grid-cols-2 [&>*]:min-w-0">
			<div class="w-full">
				<CarbonSelect label="Source" bind:value={importSource}>
					<option value="url">URL</option>
					<option value="upload">Completed upload session</option>
					<option value="backup">Existing backup</option>
				</CarbonSelect>
			</div>
			<div class="w-full">
				<CarbonTextInput label="Display name" bind:value={displayName} placeholder="My world" />
			</div>
			{#if importSource !== 'upload'}
				<div class="md:col-span-2">
					<CarbonTextInput
						label="Source URL"
						bind:value={importUrl}
						placeholder="https://…"
						helperText="Generic URL/file import is the supported fallback. Credentials are never forwarded to a download host."
					/>
				</div>
			{:else}
				<div class="md:col-span-2">
					<CarbonTextInput
						label="Upload session ID"
						bind:value={uploadId}
						placeholder="Identifier of an already completed upload"
					/>
				</div>
			{/if}
			<div>
				<CarbonTextInput
					label="Source platform"
					bind:value={sourcePlatform}
					placeholder="e.g. planetminecraft"
				/>
			</div>
			<div>
				<CarbonTextInput
					label="Expected SHA-256 (optional)"
					bind:value={sha256Checksum}
					placeholder="Whole-archive checksum"
				/>
			</div>
			<div class="md:col-span-2">
				<CarbonTextInput
					label="License attestation"
					bind:value={licenseAttestation}
					placeholder="How you are permitted to use this world"
				/>
			</div>
			<div class="md:col-span-2">
				<CarbonTextInput
					label="Idempotency key"
					bind:value={requestId}
					helperText="Reusing a key returns the same durable import job instead of creating a second one."
				/>
			</div>
		</div>

		<div>
			<CarbonButton
				kind="primary"
				size="md"
				loading={importBusy}
				disabled={!displayName.trim()}
				onclick={() => void importWorld()}
				class="justify-center gap-2"
			>
				<Upload class="h-4 w-4" />
				Journal import
			</CarbonButton>
		</div>

		{#if importError}
			<div
				role="alert"
				class="border border-destructive bg-background p-3 text-xs text-destructive-text"
			>
				{formatRpcError(importError, 'The import could not be journalled.')}
			</div>
		{/if}

		{#if importJob}
			<div class="border border-border bg-background p-4" aria-live="polite">
				<div class="flex flex-wrap items-center gap-2">
					<span class="mono-label">Import job</span>
					<CarbonTag type={importPhasePresentation(importJob.phase).tone} size="sm">
						{importPhasePresentation(importJob.phase).label}
					</CarbonTag>
					<span class="font-mono text-xs break-all text-muted-foreground">{importJob.id}</span>
				</div>
				{#if importJob.error}
					<p class="mt-2 text-xs break-all text-destructive-text">{importJob.error}</p>
				{:else if importJob.resultRevisionId}
					<p class="mt-2 font-mono text-xs break-all text-foreground">
						Revision {shortId(importJob.resultRevisionId)}
					</p>
				{:else}
					<p class="mt-2 text-xs text-muted-foreground">
						Downloading and verification are in progress. A revision is published only after the
						archive passes structural safety inspection, CRC checks and whole-archive SHA-256
						verification.
					</p>
				{/if}
				{#if importedEntryId}
					<CarbonButton
						kind="secondary"
						size="sm"
						href={resolve('/library/[id]', { id: importedEntryId })}
						class="mt-3 justify-center"
					>
						View library entry
					</CarbonButton>
				{/if}
			</div>
		{/if}
	</section>
</div>
