<!-- SPDX-License-Identifier: MPL-2.0 -->

<script lang="ts">
	import { CarbonButton, CarbonModal, CarbonTag } from '$lib/components/carbon';
	import { rpcClient } from '$lib/api/rpc-client';
	import type { IndexedModpack, ModpackFile } from '$lib/proto/carbonpanel/v1/modpack_pb';
	import { formatBytes } from '$lib/utils';
	import { formatTimestamp } from '$lib/utils/format-time';
	import { ExternalLink, Package } from '@lucide/svelte';
	import { parsePersistedStringArray } from './persisted-json';

	let {
		open = $bindable(false),
		pack,
		provider,
		onUse,
		onDelete
	}: {
		open?: boolean;
		pack: IndexedModpack | null;
		provider: string;
		onUse: (pack: IndexedModpack) => void;
		onDelete: (pack: IndexedModpack) => void;
	} = $props();

	let files = $state<ModpackFile[]>([]);
	let fileStatus = $state<'loading' | 'loaded' | 'error'>('loading');
	let retry = $state(0);
	const filesPerPage = 24;
	let shownFiles = $state(filesPerPage);
	const versions = $derived(parsePersistedStringArray(pack?.gameVersions));
	const loaders = $derived(parsePersistedStringArray(pack?.modLoaders));
	const categories = $derived(parsePersistedStringArray(pack?.categories));

	function webUrl(value: string | undefined): string | undefined {
		if (!value) return;
		try {
			const url = new URL(value);
			if (
				(url.protocol === 'https:' || url.protocol === 'http:') &&
				!url.username &&
				!url.password
			) {
				return url.href;
			}
		} catch {
			return;
		}
	}

	const website = $derived(webUrl(pack?.websiteUrl));
	const logo = $derived(webUrl(pack?.logoUrl));
	const runtimeDetails = $derived(
		pack
			? [
					{ name: 'Default Minecraft version', value: pack.mcVersion },
					{ name: 'Java', value: pack.javaVersion > 0 ? String(pack.javaVersion) : '' },
					{
						name: 'Recommended RAM',
						value: pack.recommendedRam > 0 ? formatBytes(pack.recommendedRam * 1024 ** 2) : ''
					},
					{ name: 'Server image', value: pack.dockerImage }
				].filter(({ value }) => value)
			: []
	);

	$effect(() => {
		void retry;
		if (!open || !pack?.id) return;
		const controller = new AbortController();
		files = [];
		shownFiles = filesPerPage;
		fileStatus = 'loading';
		void rpcClient.modpack
			.getModpackFiles({ id: pack.id }, { signal: controller.signal })
			.then((response) => {
				if (controller.signal.aborted) return;
				files = response.files;
				fileStatus = 'loaded';
			})
			.catch(() => {
				if (!controller.signal.aborted) fileStatus = 'error';
			});
		return () => controller.abort();
	});
</script>

<CarbonModal bind:open title={pack?.name || 'Modpack details'} size="2xl">
	{#if pack}
		<div class="space-y-6" data-testid="modpack-details">
			<div class="flex items-start gap-4">
				{#if logo}
					<img
						src={logo}
						alt=""
						class="size-14 shrink-0 border border-border bg-card object-cover"
					/>
				{:else}
					<span class="grid size-14 shrink-0 place-items-center border border-border bg-card">
						<Package class="size-6 text-muted-foreground" aria-hidden="true" />
					</span>
				{/if}
				<div class="min-w-0 space-y-2">
					<div class="flex flex-wrap items-center gap-2">
						<CarbonTag type={pack.indexer === 'manual' ? 'purple' : 'blue'} size="sm">
							{provider}
						</CarbonTag>
						{#if pack.indexer !== 'manual'}
							<span class="text-sm text-muted-foreground">
								{pack.downloadCount.toLocaleString()} downloads
							</span>
						{/if}
						{#if pack.isFavorited}
							<CarbonTag type="gray" size="sm">Favorite</CarbonTag>
						{/if}
					</div>
					{#if pack.summary}
						<p class="text-sm leading-relaxed text-foreground">{pack.summary}</p>
					{/if}
				</div>
			</div>

			{#if pack.description && pack.description.trim() !== pack.summary.trim()}
				<section class="space-y-2">
					<h3 class="text-sm font-semibold">Description</h3>
					<p class="text-sm leading-relaxed break-words whitespace-pre-wrap text-muted-foreground">
						{pack.description}
					</p>
				</section>
			{/if}

			<section class="space-y-3">
				<h3 class="text-sm font-semibold">Compatibility</h3>
				<dl class="grid gap-3 border border-border bg-card p-4 sm:grid-cols-2">
					<div>
						<dt class="text-xs text-muted-foreground">Minecraft versions</dt>
						<dd class="mt-1 text-sm break-words">
							{versions.values.join(', ') || (versions.malformed ? 'Unreadable' : 'Not listed')}
						</dd>
					</div>
					<div>
						<dt class="text-xs text-muted-foreground">Loaders</dt>
						<dd class="mt-1 text-sm break-words">
							{loaders.values.join(', ') || (loaders.malformed ? 'Unreadable' : 'Not listed')}
						</dd>
					</div>
					{#each runtimeDetails as detail (detail.name)}
						<div>
							<dt class="text-xs text-muted-foreground">{detail.name}</dt>
							<dd class="mt-1 text-sm break-words">{detail.value}</dd>
						</div>
					{/each}
				</dl>
				{#if categories.values.length || categories.malformed}
					<p class="text-sm text-muted-foreground">
						Categories <span class="text-foreground"
							>{categories.values.join(', ') || 'Unreadable'}</span
						>
					</p>
				{/if}
				<dl class="flex flex-wrap gap-x-6 gap-y-2 text-xs text-muted-foreground">
					{#each [{ name: 'Created', date: pack.dateCreated }, { name: 'Updated', date: pack.dateModified }, { name: 'Released', date: pack.dateReleased }].filter(({ date }) => date && date.seconds > 0n) as entry (entry.name)}
						<div>
							<dt>{entry.name}</dt>
							<dd class="mt-1 text-foreground">{formatTimestamp(entry.date)}</dd>
						</div>
					{/each}
				</dl>
				{#if pack.indexError}
					<p class="border-l-2 border-warning bg-card p-3 text-sm text-warning-text">
						Catalogue warning: {pack.indexError}
					</p>
				{/if}
			</section>

			<section class="space-y-2">
				<h3 class="text-sm font-semibold">Indexed files</h3>
				{#if fileStatus === 'loading'}
					<p class="text-sm text-muted-foreground" role="status">Loading files…</p>
				{:else if fileStatus === 'error'}
					<div class="flex flex-wrap items-center gap-2" role="status">
						<p class="text-sm text-muted-foreground">Files could not be loaded.</p>
						<CarbonButton kind="tertiary" size="sm" onclick={() => (retry += 1)}>Retry</CarbonButton
						>
					</div>
				{:else if files.length === 0}
					<p class="text-sm text-muted-foreground">No files indexed for this pack yet.</p>
				{:else}
					<ul class="divide-y divide-border border border-border bg-card">
						{#each files.slice(0, shownFiles) as file (file.id)}
							<li class="space-y-1 px-3 py-2.5">
								<div class="flex flex-wrap items-center gap-2">
									<p class="text-sm font-medium break-all">
										{file.displayName || file.fileName || file.id}
									</p>
									{#if file.id === pack.latestFileId}
										<CarbonTag type="blue" size="sm">Latest</CarbonTag>
									{/if}
								</div>
								<p class="text-xs break-words text-muted-foreground">
									{[
										file.displayName && file.fileName !== file.displayName ? file.fileName : '',
										file.releaseType,
										file.fileLength > 0n ? formatBytes(Number(file.fileLength)) : '',
										file.fileDate && file.fileDate.seconds > 0n
											? formatTimestamp(file.fileDate)
											: '',
										file.gameVersions.join(', ')
									]
										.filter(Boolean)
										.join(' · ')}
								</p>
								{#if file.sha256}
									<p class="text-xs break-all text-muted-foreground">SHA-256 {file.sha256}</p>
								{/if}
							</li>
						{/each}
					</ul>
					{#if shownFiles < files.length}
						<div class="flex flex-wrap items-center justify-between gap-2">
							<p class="text-xs text-muted-foreground">
								Showing {shownFiles} of {files.length} files
							</p>
							<CarbonButton kind="secondary" size="sm" onclick={() => (shownFiles += filesPerPage)}>
								Show more files
							</CarbonButton>
						</div>
					{/if}
				{/if}
			</section>
		</div>
	{/if}

	{#snippet footer()}
		{#if pack}
			<div class="flex flex-wrap items-center justify-end gap-2">
				{#if pack.indexer === 'manual'}
					<CarbonButton kind="tertiary" size="sm" onclick={() => pack && onDelete(pack)}>
						Delete imported pack
					</CarbonButton>
				{/if}
				{#if website}
					<CarbonButton
						kind="tertiary"
						size="sm"
						href={website}
						target="_blank"
						rel="noopener noreferrer"
					>
						Provider page <ExternalLink class="ml-1.5 size-3.5" aria-hidden="true" />
					</CarbonButton>
				{/if}
				<CarbonButton kind="primary" size="sm" onclick={() => pack && onUse(pack)}>
					Use in Server
				</CarbonButton>
			</div>
		{/if}
	{/snippet}
</CarbonModal>
