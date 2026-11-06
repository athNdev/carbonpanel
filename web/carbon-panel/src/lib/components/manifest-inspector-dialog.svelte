<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import {
		CarbonButton,
		CarbonModal,
		CarbonSearch,
		CarbonStructuredList,
		CarbonTag,
		CarbonTile
	} from '$lib/components/carbon';
	import { toast } from 'svelte-sonner';
	import { inspectManifest, exportServerManifest } from '$lib/utils/manifest-inspector';
	import type { ManifestInspectionResult } from '$lib/utils/manifest-inspector';
	import {
		FileSearch,
		Upload,
		Copy,
		Download,
		Laptop,
		Server,
		Layers,
		ExternalLink
	} from '@lucide/svelte';

	interface Props {
		open: boolean;
		onOpenChange: (open: boolean) => void;
	}

	let { open = $bindable(false), onOpenChange }: Props = $props();

	let rawInput = $state('');
	let inspection = $state<ManifestInspectionResult | null>(null);
	let error = $state<string | null>(null);
	let filter = $state<'all' | 'both' | 'client' | 'server'>('all');
	let searchQuery = $state('');

	function handleInspect() {
		error = null;
		if (!rawInput.trim()) {
			error = 'Please paste manifest JSON or upload a manifest file.';
			return;
		}
		try {
			inspection = inspectManifest(rawInput);
			toast.success(`Parsed ${inspection.name} (${inspection.totalMods} mods)`);
		} catch (e) {
			error = (e instanceof Error ? e.message : undefined) || 'Failed to parse manifest.';
			inspection = null;
		}
	}

	function handleFileUpload(e: Event) {
		const target = e.target as HTMLInputElement;
		const file = target.files?.[0];
		if (!file) return;

		const reader = new FileReader();
		reader.onload = (event) => {
			rawInput = (event.target?.result as string) || '';
			handleInspect();
		};
		reader.readAsText(file);
	}

	function copyServerManifest() {
		if (!inspection) return;
		const exportText = exportServerManifest(inspection);
		navigator.clipboard.writeText(exportText);
		toast.success('Server-ready manifest copied to clipboard!');
	}

	function downloadServerManifest() {
		if (!inspection) return;
		const exportText = exportServerManifest(inspection);
		const filename =
			inspection.format === 'modrinth' ? 'modrinth.server.index.json' : 'manifest.server.json';
		const blob = new Blob([exportText], { type: 'application/json' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = filename;
		a.click();
		URL.revokeObjectURL(url);
		toast.success(`Downloaded ${filename}`);
	}

	let filteredMods = $derived.by(() => {
		if (!inspection) return [];
		return inspection.mods.filter((m) => {
			const matchesFilter =
				filter === 'all' ||
				(filter === 'both' && m.env === 'both') ||
				(filter === 'client' && m.env === 'client') ||
				(filter === 'server' && m.env === 'server');

			const q = searchQuery.toLowerCase().trim();
			const matchesQuery =
				!q ||
				m.name.toLowerCase().includes(q) ||
				(m.filename && m.filename.toLowerCase().includes(q));

			return matchesFilter && matchesQuery;
		});
	});

	let serverCompatible = $derived(
		inspection ? inspection.universalCount + inspection.serverOnlyCount : 0
	);
	let serverPct = $derived(
		inspection ? Math.round((serverCompatible / Math.max(1, inspection.totalMods)) * 100) : 0
	);
</script>

<CarbonModal
	bind:open
	title="CurseForge and Modrinth Manifest Inspector"
	description="Inspect modpack manifests, detect client-only vs server-side mods, and export server-clean manifests."
	size="4xl"
	hasFooter={false}
	onclose={() => onOpenChange?.(false)}
>
	{#if !inspection}
		<div class="flex flex-col gap-4 py-2">
			<div class="flex flex-wrap items-center gap-3">
				<label class="cursor-pointer">
					<input type="file" accept=".json,.mrpack" class="hidden" onchange={handleFileUpload} />
					<span
						class="inline-flex items-center gap-2 rounded-none border border-foreground px-4 py-2 text-sm transition-colors hover:bg-accent"
					>
						<Upload class="h-4 w-4" /> Upload manifest.json / modrinth.index.json
					</span>
				</label>
				<span class="text-xs text-muted-foreground">or paste JSON content below:</span>
			</div>

			<textarea
				bind:value={rawInput}
				rows={10}
				placeholder="Paste manifest.json or modrinth.index.json contents here..."
				class="w-full rounded-none border-b border-border bg-muted p-3 font-mono text-xs text-foreground focus:border-primary focus:outline-none"
			></textarea>

			{#if error}
				<div
					class="rounded-none border-l-4 border-destructive bg-destructive/10 p-3 text-xs text-destructive"
					role="alert"
				>
					{error}
				</div>
			{/if}

			<CarbonButton kind="primary" onclick={handleInspect} class="w-full justify-center">
				<FileSearch class="mr-2 h-4 w-4" />
				Inspect Manifest
			</CarbonButton>
		</div>
	{:else}
		<div class="flex flex-col gap-4 py-2">
			<div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
				<CarbonTile class="rounded-none p-3">
					<div class="text-xs text-muted-foreground">Format</div>
					<div class="mt-0.5">
						<CarbonTag type="cyan" size="sm">{inspection.format}</CarbonTag>
					</div>
					<div class="mt-1 truncate text-xs text-muted-foreground">{inspection.name}</div>
				</CarbonTile>
				<CarbonTile class="rounded-none p-3">
					<div class="text-xs text-muted-foreground">Game and Loader</div>
					<div class="truncate text-sm font-semibold">{inspection.gameVersion}</div>
					<div class="mt-1 truncate text-xs text-muted-foreground">{inspection.modLoader}</div>
				</CarbonTile>
				<CarbonTile class="rounded-none p-3">
					<div class="text-xs text-muted-foreground">Server-Compatible</div>
					<div class="mt-0.5">
						<CarbonTag type="green" size="sm">{serverCompatible} mods</CarbonTag>
					</div>
					<div class="mt-1 text-xs text-muted-foreground">{serverPct}% of total</div>
				</CarbonTile>
				<CarbonTile class="rounded-none p-3">
					<div class="text-xs text-muted-foreground">Client-Only (Omit)</div>
					<div class="mt-0.5">
						<CarbonTag type="yellow" size="sm">{inspection.clientOnlyCount} mods</CarbonTag>
					</div>
					<div class="mt-1 text-xs text-muted-foreground">Safe to exclude on server</div>
				</CarbonTile>
			</div>

			<div class="flex flex-wrap items-center justify-between gap-2">
				<div
					class="flex flex-wrap items-center gap-1.5"
					role="group"
					aria-label="Environment filter"
				>
					<CarbonButton
						size="sm"
						kind={filter === 'all' ? 'primary' : 'secondary'}
						class="rounded-none"
						onclick={() => (filter = 'all')}
					>
						All ({inspection.totalMods})
					</CarbonButton>
					<CarbonButton
						size="sm"
						kind={filter === 'both' ? 'primary' : 'secondary'}
						class="rounded-none"
						onclick={() => (filter = 'both')}
					>
						Universal ({inspection.universalCount})
					</CarbonButton>
					<CarbonButton
						size="sm"
						kind={filter === 'client' ? 'primary' : 'secondary'}
						class="rounded-none"
						onclick={() => (filter = 'client')}
					>
						Client-Only ({inspection.clientOnlyCount})
					</CarbonButton>
					<CarbonButton
						size="sm"
						kind={filter === 'server' ? 'primary' : 'secondary'}
						class="rounded-none"
						onclick={() => (filter = 'server')}
					>
						Server-Only ({inspection.serverOnlyCount})
					</CarbonButton>
				</div>

				<div class="w-48">
					<CarbonSearch placeholder="Search mods..." bind:value={searchQuery} size="sm" />
				</div>
			</div>

			{#if filteredMods.length === 0}
				<div class="py-8 text-center text-sm text-muted-foreground">
					No mods match the current filter.
				</div>
			{:else}
				<CarbonStructuredList class="max-h-64 overflow-y-auto">
					{#snippet header()}
						<div class="grid w-full grid-cols-[1fr_auto_auto] items-center gap-3">
							<span>Mod Name</span>
							<span>Environment</span>
							<span>Action</span>
						</div>
					{/snippet}
					{#each filteredMods as mod (mod.path ?? mod.name)}
						<div
							class="grid w-full grid-cols-[1fr_auto_auto] items-center gap-3 px-4 py-2.5 text-xs"
						>
							<span class="min-w-0">
								<span class="block truncate font-semibold">{mod.name}</span>
								{#if mod.filename && mod.filename !== mod.name}
									<span class="block truncate font-mono text-muted-foreground">
										{mod.filename}
									</span>
								{/if}
								<span class="block text-muted-foreground">
									{mod.required ? 'Required' : 'Optional'}
								</span>
							</span>
							<span>
								{#if mod.env === 'client'}
									<CarbonTag type="yellow" size="sm">
										<Laptop class="h-3 w-3" /> Client Only
									</CarbonTag>
								{:else if mod.env === 'server'}
									<CarbonTag type="blue" size="sm">
										<Server class="h-3 w-3" /> Server Only
									</CarbonTag>
								{:else}
									<CarbonTag type="green" size="sm">
										<Layers class="h-3 w-3" /> Universal
									</CarbonTag>
								{/if}
							</span>
							<span class="text-right">
								{#if mod.downloadUrl}
									<!-- eslint-disable svelte/no-navigation-without-resolve -- external download URL used directly; resolve() throws on non-absolute pathnames -->
									<a
										href={mod.downloadUrl}
										target="_blank"
										rel="noreferrer"
										class="inline-flex items-center gap-1 text-xs text-primary hover:underline"
									>
										<ExternalLink class="h-3 w-3" /> Link
									</a>
									<!-- eslint-enable svelte/no-navigation-without-resolve -->
								{:else if mod.projectId}
									<span class="font-mono text-xs text-muted-foreground">
										#{mod.projectId}
									</span>
								{/if}
							</span>
						</div>
					{/each}
				</CarbonStructuredList>
			{/if}

			<div class="flex flex-wrap items-center justify-between gap-2 border-t border-border pt-3">
				<CarbonButton kind="secondary" size="sm" onclick={() => (inspection = null)}>
					Inspect Another
				</CarbonButton>
				<div class="flex items-center gap-2">
					<CarbonButton kind="secondary" size="sm" onclick={copyServerManifest}>
						<Copy class="mr-1.5 h-3.5 w-3.5" /> Copy Server Manifest
					</CarbonButton>
					<CarbonButton kind="primary" size="sm" onclick={downloadServerManifest}>
						<Download class="mr-1.5 h-3.5 w-3.5" /> Export Clean Manifest
					</CarbonButton>
				</div>
			</div>
		</div>
	{/if}
</CarbonModal>
