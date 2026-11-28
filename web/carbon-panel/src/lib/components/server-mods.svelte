<!--
SPDX-License-Identifier: MIT AND MPL-2.0
This file includes DiscoPanel material under the MIT License,
Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
and still applies to that inherited material.
Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
If a copy of the MPL was not distributed with this file, You can obtain
one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import {
		Loader2,
		Upload,
		Download,
		Trash2,
		Package,
		FileText,
		X,
		Boxes,
		Search,
		Check,
		Ban,
		RefreshCw
	} from '@lucide/svelte';
	import { rpcClient, wasRpcErrorToasted } from '$lib/api/rpc-client';
	import { toast } from 'svelte-sonner';
	import { ModLoader, type Server } from '$lib/proto/carbonpanel/v1/common_pb';
	import type { Mod } from '$lib/proto/carbonpanel/v1/mod_pb';
	import { formatBytes } from '$lib/utils';
	import { summarizeModUpdateCheck } from '$lib/utils/mod-update-summary';
	import { uploadFile, cancelUpload, type UploadProgress } from '$lib/utils/chunked-upload';
	import ModBrowserDialog from '$lib/components/mod-browser-dialog.svelte';
	import {
		CarbonTag,
		CarbonInlineLoading,
		CarbonButton,
		CarbonConfirm
	} from '$lib/components/carbon';
	import ServerTabPanel from '$lib/components/server-ui/ServerTabPanel.svelte';

	interface Props {
		server: Server;
		active?: boolean;
	}

	let { server, active = false }: Props = $props();

	let mods = $state<Mod[]>([]);
	let loading = $state(true);
	let uploading = $state(false);
	let uploadProgress = $state<UploadProgress | null>(null);
	let currentUploadFilename = $state('');
	let uploadAbortController = $state<AbortController | null>(null);
	let fileInput = $state<HTMLInputElement | null>(null);
	let browserDialogOpen = $state(false);

	let searchQuery = $state('');
	let modFilter = $state<'all' | 'enabled' | 'disabled'>('all');

	let hasLoaded = false;
	let requestNumber = 0;
	let updateStatus = $state('');
	// A check runs against an already-listed set. Keeping it separate from
	// `loading` leaves the rows on screen while the provider is queried.
	let checking = $state(false);
	let previousServerId = '';

	$effect(() => {
		if (server.id !== previousServerId) {
			previousServerId = server.id;
			updateStatus = '';
			checking = false;
			mods = [];
			loading = true;
			uploading = false;
			hasLoaded = false;
		}
		if (active && !hasLoaded) {
			hasLoaded = true;
			loadMods();
		}
	});

	let filteredMods = $derived.by(() => {
		let result = [...mods];
		if (modFilter === 'enabled') {
			result = result.filter((m) => m.enabled);
		} else if (modFilter === 'disabled') {
			result = result.filter((m) => !m.enabled);
		}

		if (searchQuery.trim()) {
			const q = searchQuery.toLowerCase().trim();
			result = result.filter(
				(m) =>
					m.displayName.toLowerCase().includes(q) ||
					m.fileName.toLowerCase().includes(q) ||
					(m.description && m.description.toLowerCase().includes(q))
			);
		}
		return result;
	});

	function startUpdateCheck() {
		if (checking || loading || uploading || !mods.length) return;
		void loadMods(true);
	}

	async function loadMods(checkUpdates = false) {
		const serverId = server.id;
		const request = ++requestNumber;
		const isCurrent = () => request === requestNumber && server.id === serverId;
		// The check trigger keeps focus while a check runs: it is only
		// aria-disabled, never natively disabled, so Enter/Space do not send a
		// keyboard user back to the top of the page. Restore focus defensively
		// if an unrelated path did drop it to the document.
		const focused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
		checking = checkUpdates;
		updateStatus = checkUpdates ? 'Checking installed files for compatible releases…' : '';
		// A vanilla server has no mods directory, and asking for one returns an
		// error the RPC interceptor turns into a toast - on a tab that already
		// explains the server cannot take mods. Do not make the call.
		if (!canHaveMods()) {
			mods = [];
			loading = false;
			checking = false;
			return;
		}
		try {
			// A check keeps the current rows visible; only the first load (or a
			// mutation-triggered refresh) replaces them with the loading state.
			if (!checkUpdates) loading = true;
			const response = await rpcClient.mod.listMods({ serverId, checkUpdates });
			if (!isCurrent()) return;
			mods = response.mods;
			if (checkUpdates) {
				updateStatus = summarizeModUpdateCheck(mods.map((mod) => mod.updatePreview?.state));
			}
		} catch (_e) {
			if (isCurrent()) {
				updateStatus = checkUpdates ? 'Update check failed. Try again.' : '';
				if (!wasRpcErrorToasted(_e)) {
					toast.error(checkUpdates ? 'Failed to check for updates' : 'Failed to load mods');
				}
			}
		} finally {
			if (isCurrent()) {
				loading = false;
				checking = false;
				if (focused && !document.activeElement?.isConnected) focused.focus();
				else if (focused && document.activeElement === document.body) focused.focus();
			}
		}
	}

	async function handleFileSelect(event: Event) {
		const input = event.target as HTMLInputElement;
		const fileList = input.files;
		if (!fileList || fileList.length === 0) return;

		uploading = true;
		uploadAbortController = new AbortController();

		try {
			for (const file of Array.from(fileList)) {
				currentUploadFilename = file.name;
				uploadProgress = null;

				const result = await uploadFile(file, {
					onProgress: (progress) => {
						uploadProgress = progress;
					},
					signal: uploadAbortController.signal
				});

				await rpcClient.mod.importUploadedMod({
					serverId: server.id,
					uploadSessionId: result.sessionId,
					displayName: file.name,
					description: ''
				});
			}
			toast.success(`Uploaded ${fileList.length} mod(s)`);
			await loadMods();
		} catch (error: unknown) {
			if (error instanceof Error && error.message === 'Upload cancelled') {
				toast.info('Upload cancelled');
			} else if (!wasRpcErrorToasted(error)) {
				toast.error('Failed to upload mod');
			}
		} finally {
			uploading = false;
			uploadProgress = null;
			currentUploadFilename = '';
			uploadAbortController = null;
			input.value = '';
		}
	}

	function cancelCurrentUpload() {
		if (uploadAbortController) {
			uploadAbortController.abort();
		}
		if (uploadProgress?.sessionId) {
			cancelUpload(uploadProgress.sessionId).catch(() => {});
		}
	}

	async function toggleMod(mod: Mod) {
		try {
			await rpcClient.mod.updateMod({
				serverId: server.id,
				modId: mod.id,
				enabled: !mod.enabled,
				displayName: mod.displayName,
				description: mod.description
			});
			toast.success(`Mod ${!mod.enabled ? 'enabled' : 'disabled'}`);
			await loadMods();
		} catch (_e) {
			if (!wasRpcErrorToasted(_e)) toast.error('Failed to toggle mod');
		}
	}

	let pendingDelete = $state<Mod | null>(null);
	let confirmOpen = $state(false);

	async function deleteMod(mod: Mod) {
		pendingDelete = mod;
		confirmOpen = true;
	}

	async function confirmDeleteMod() {
		const mod = pendingDelete;
		if (!mod) return;
		try {
			await rpcClient.mod.deleteMod({
				serverId: server.id,
				modId: mod.id
			});
			toast.success('Mod deleted');
			await loadMods();
		} catch (_e) {
			if (!wasRpcErrorToasted(_e)) toast.error('Failed to delete mod');
		} finally {
			pendingDelete = null;
		}
	}

	async function downloadMod(mod: Mod) {
		try {
			const response = await rpcClient.file.getFile({
				serverId: server.id,
				path: `${getModsDirectory()}/${mod.fileName}`
			});
			const blob = new Blob([new Uint8Array(response.content)]);
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = mod.fileName;
			a.click();
			URL.revokeObjectURL(url);
		} catch (_e) {
			if (!wasRpcErrorToasted(_e)) toast.error('Failed to download mod');
		}
	}

	function getModsDirectory(): string {
		const modLoaderInfo: Record<ModLoader, string> = {
			[ModLoader.UNSPECIFIED]: 'mods',
			[ModLoader.VANILLA]: 'mods',
			[ModLoader.FORGE]: 'mods',
			[ModLoader.NEOFORGE]: 'mods',
			[ModLoader.FABRIC]: 'mods',
			[ModLoader.QUILT]: 'mods',
			[ModLoader.BUKKIT]: 'plugins',
			[ModLoader.SPIGOT]: 'plugins',
			[ModLoader.PAPER]: 'plugins',
			[ModLoader.PURPUR]: 'plugins',
			[ModLoader.SPONGE_VANILLA]: 'mods',
			[ModLoader.SPONGE_FORGE]: 'mods',
			[ModLoader.MOHIST]: 'mods',
			[ModLoader.CATSERVER]: 'mods',
			[ModLoader.ARCLIGHT]: 'mods',
			[ModLoader.AUTO_CURSEFORGE]: 'mods',
			[ModLoader.MODRINTH]: 'mods',
			[ModLoader.FOLIA]: 'plugins'
		};

		return modLoaderInfo[server.modLoader] || 'mods';
	}

	function canHaveMods(): boolean {
		const noModLoaders = [ModLoader.VANILLA, ModLoader.UNSPECIFIED];
		return !noModLoaders.includes(server.modLoader);
	}
</script>

<ServerTabPanel
	title="Mod & Plugin Management"
	description={canHaveMods()
		? `Manage extensions in the container ${getModsDirectory()} directory`
		: 'Vanilla Minecraft does not support external mod injection'}
>
	{#snippet chip()}
		<CarbonTag type="teal" size="sm">{getModsDirectory()}/</CarbonTag>
	{/snippet}
	{#snippet actions()}
		{#if canHaveMods()}
			<!--
				Icon-only below sm. The full label otherwise wraps onto a third
				action row at 390 px and takes the height the mod list needs. One
				element only, so the tab order and accessible name stay stable.
			-->
			<CarbonButton
				kind="secondary"
				size="sm"
				class="max-sm:px-2! {checking ? 'opacity-50' : ''}"
				disabled={loading || uploading || !mods.length}
				aria-disabled={checking ? 'true' : undefined}
				aria-label="Check for updates"
				title="Check for updates"
				onclick={startUpdateCheck}
			>
				<RefreshCw class="h-4 w-4 sm:hidden {checking ? 'animate-spin' : ''}" />
				<span class="hidden sm:inline">Check for updates</span>
			</CarbonButton>
			<CarbonButton kind="primary" size="sm" onclick={() => (browserDialogOpen = true)}>
				<Boxes class="h-4 w-4" />
				<span>Browse Mods</span>
			</CarbonButton>
			<CarbonButton
				kind="secondary"
				size="sm"
				onclick={() => fileInput?.click()}
				disabled={uploading}
			>
				{#if uploading}
					<Loader2 class="h-4 w-4 animate-spin" />
				{:else}
					<Upload class="h-4 w-4" />
				{/if}
				<span>Upload .jar</span>
			</CarbonButton>
			<input
				bind:this={fileInput}
				type="file"
				multiple
				accept=".jar,.zip"
				onchange={handleFileSelect}
				class="hidden"
			/>
		{/if}
	{/snippet}
	<p role="status" class="px-3 text-sm text-muted-foreground">{updateStatus}</p>
	{#snippet toolbar()}
		{#if canHaveMods()}
			<div
				class="flex flex-col items-stretch justify-between gap-3 p-3 sm:flex-row sm:items-center"
			>
				<div class="relative max-w-md flex-1">
					<Search
						class="absolute top-1/2 left-3 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground"
					/>
					<input
						type="search"
						placeholder="Search installed mods..."
						bind:value={searchQuery}
						onkeydown={(e) => e.key === 'Escape' && (searchQuery = '')}
						class="h-8 w-full rounded-none border-b border-border bg-card pr-3 pl-8 text-xs text-foreground placeholder-muted-foreground focus:border-b-2 focus:border-primary focus:outline-none"
					/>
				</div>
				<div class="flex items-center gap-1">
					<button
						type="button"
						onclick={() => (modFilter = 'all')}
						aria-pressed={modFilter === 'all'}
						class="cursor-pointer rounded-none border px-2.5 py-1 font-mono text-xs uppercase transition-colors {modFilter ===
						'all'
							? 'border-primary bg-primary text-white'
							: 'border-border bg-card text-muted-foreground hover:bg-secondary'}"
					>
						All ({mods.length})
					</button>
					<button
						type="button"
						onclick={() => (modFilter = 'enabled')}
						aria-pressed={modFilter === 'enabled'}
						class="cursor-pointer rounded-none border px-2.5 py-1 font-mono text-xs uppercase transition-colors {modFilter ===
						'enabled'
							? 'border-[#198038] bg-[#198038] text-white'
							: 'border-success/50 bg-card text-success-text hover:bg-secondary'}"
					>
						Enabled ({mods.filter((m) => m.enabled).length})
					</button>
					<button
						type="button"
						onclick={() => (modFilter = 'disabled')}
						aria-pressed={modFilter === 'disabled'}
						class="cursor-pointer rounded-none border px-2.5 py-1 font-mono text-xs uppercase transition-colors {modFilter ===
						'disabled'
							? 'border-border bg-secondary text-foreground'
							: 'border-border/60 bg-card text-muted-foreground hover:bg-secondary'}"
					>
						Disabled ({mods.filter((m) => !m.enabled).length})
					</button>
				</div>
			</div>
		{/if}
		{#if uploading && uploadProgress}
			<div class="border-t border-border bg-card p-3 text-xs">
				<div class="mb-1 flex items-center justify-between">
					<span class="truncate font-mono text-foreground">Uploading: {currentUploadFilename}</span>
					<div class="flex items-center gap-2">
						<span class="font-mono text-accent-text"
							>{uploadProgress.percentComplete.toFixed(0)}%</span
						>
						<button
							type="button"
							onclick={cancelCurrentUpload}
							class="p-0.5 text-destructive-text hover:text-foreground"
							title="Cancel upload"
							aria-label="Cancel upload"
						>
							<X class="h-3.5 w-3.5" />
						</button>
					</div>
				</div>
				<div class="h-1.5 w-full rounded-none border border-border bg-background">
					<div
						class="h-full rounded-none bg-primary transition-all"
						style="width: {uploadProgress.percentComplete}%"
					></div>
				</div>
				<p class="mt-1 font-mono text-xs text-muted-foreground">
					{formatBytes(uploadProgress.bytesUploaded)} / {formatBytes(uploadProgress.totalBytes)}
				</p>
			</div>
		{/if}
	{/snippet}
	<div class="min-h-0 flex-1 overflow-auto bg-background" aria-busy={checking ? 'true' : undefined}>
		{#if !canHaveMods()}
			<div class="flex flex-col items-center justify-center p-12 text-center text-muted-foreground">
				<Package class="mb-3 h-10 w-10 text-muted-foreground" />
				<p class="text-sm font-semibold text-foreground">Mod loader incompatible</p>
				<p class="mt-1 max-w-sm text-xs text-muted-foreground">
					To install mods or plugins, configure this server to use Fabric, Forge, NeoForge, Paper,
					or Purpur.
				</p>
			</div>
		{:else if loading}
			<div class="flex items-center justify-center p-12">
				<CarbonInlineLoading description="Reading {getModsDirectory()}/ directory..." />
			</div>
		{:else if mods.length === 0}
			<div class="flex flex-col items-center justify-center p-12 text-center text-muted-foreground">
				<Package class="mb-3 h-10 w-10 text-muted-foreground" />
				<p class="text-sm font-semibold text-foreground">No mods installed</p>
				<p class="mt-1 text-xs text-muted-foreground">
					Browse the repository or upload .jar files directly to get started.
				</p>
			</div>
		{:else if filteredMods.length === 0}
			<div class="p-8 text-center text-xs text-muted-foreground">
				No mods match your search query or filter.
			</div>
		{:else}
			<!-- Carbon DataTable rows -->
			<div class="divide-y divide-border border-b border-border">
				{#each filteredMods as mod (mod.id)}
					<div
						class="group flex items-center justify-between gap-4 rounded-none bg-card p-3.5 transition-colors hover:bg-secondary"
					>
						<div class="flex min-w-0 items-center gap-3.5">
							<!-- Enable/Disable Carbon Toggle -->
							<button
								type="button"
								onclick={() => toggleMod(mod)}
								class="flex h-7 cursor-pointer items-center gap-1 rounded-none border px-2 font-mono text-xs transition-colors select-none {mod.enabled
									? 'border-success bg-success/20 text-success-text'
									: 'border-border bg-background text-muted-foreground'}"
								title={mod.enabled ? 'Click to disable' : 'Click to enable'}
							>
								{#if mod.enabled}
									<Check class="h-3 w-3" />
									<span class="text-xs uppercase">Active</span>
								{:else}
									<Ban class="h-3 w-3" />
									<span class="text-xs uppercase">Disabled</span>
								{/if}
							</button>

							<!-- Info -->
							<div class="min-w-0">
								<div class="flex items-center gap-2">
									<h4
										class="truncate text-sm font-medium text-foreground {mod.enabled
											? ''
											: 'line-through opacity-70'}"
									>
										{mod.displayName}
									</h4>
									{#if mod.version}
										<span
											class="border border-border bg-background px-1.5 py-0 font-mono text-xs text-muted-foreground"
										>
											{mod.version}
										</span>
									{/if}
								</div>
								<div
									class="mt-0.5 flex flex-wrap items-center gap-3 font-mono text-xs text-muted-foreground"
								>
									<span class="flex items-center gap-1 text-muted-foreground">
										<FileText class="h-3 w-3" />
										{mod.fileName}
									</span>
									<span>{formatBytes(Number(mod.fileSize))}</span>
									{#if mod.uploadedAt}
										<span
											>{new Date(Number(mod.uploadedAt.seconds) * 1000).toLocaleDateString()}</span
										>
									{/if}
								</div>
								{#if mod.updatePreview}
									{#if mod.updatePreview.installedVersion}
										<p class="mt-1 text-xs break-words text-muted-foreground">
											Modrinth · Installed {mod.updatePreview.installedVersion}
											{#if mod.updatePreview.candidateVersion && mod.updatePreview.candidateVersionId !== mod.updatePreview.installedVersionId}
												· Candidate {mod.updatePreview.candidateVersion}
											{/if}
										</p>
									{/if}
									<p class="mt-1 text-sm text-muted-foreground">{mod.updatePreview.reason}</p>
								{/if}
								{#if mod.description}
									<p class="mt-1 line-clamp-1 font-sans text-xs text-muted-foreground">
										{mod.description}
									</p>
								{/if}
							</div>
						</div>

						<!-- Actions -->
						<div class="flex shrink-0 items-center border border-border bg-background">
							<button
								type="button"
								onclick={() => downloadMod(mod)}
								class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none border-r border-border text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
								title="Download .jar"
							>
								<Download class="h-3.5 w-3.5" />
							</button>
							<button
								type="button"
								onclick={() => deleteMod(mod)}
								class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none text-destructive-text transition-colors hover:bg-destructive/20"
								title="Delete mod"
							>
								<Trash2 class="h-3.5 w-3.5" />
							</button>
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</div>
</ServerTabPanel>

<ModBrowserDialog bind:open={browserDialogOpen} {server} onInstalled={loadMods} />
<CarbonConfirm
	bind:open={confirmOpen}
	title="Delete mod?"
	message="The file is removed from the server. This cannot be undone."
	confirmLabel="Delete Mod"
	danger
	onconfirm={confirmDeleteMod}
	onclose={() => (pendingDelete = null)}
>
	{#snippet details()}
		{#if pendingDelete}{pendingDelete.displayName}{/if}
	{/snippet}
</CarbonConfirm>
