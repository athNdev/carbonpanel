<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { CarbonButton } from '$lib/components/carbon';
	import { Card, CardContent } from '$lib/components/ui/card';
	import { Badge } from '$lib/components/ui/badge';
	import { Database, Download, Trash2, Loader2, Package, RefreshCw, Upload } from '@lucide/svelte';
	import { toast } from 'svelte-sonner';
	import type { Server } from '$lib/proto/carbonpanel/v1/common_pb';
	import { rpcClient } from '$lib/api/rpc-client';
	import { uploadFile } from '$lib/utils/chunked-upload';
	import DatapackBrowserDialog from '$lib/components/datapack-browser-dialog.svelte';

	interface Props {
		server: Server;
	}

	let { server }: Props = $props();

	let datapacks = $state<
		Array<{
			fileName: string;
			displayName: string;
			size: number;
			packFormat: string;
			formatWarning: string;
		}>
	>([]);
	let loading = $state(false);
	let error = $state('');
	let browseOpen = $state(false);
	let deleting = $state<Record<string, boolean>>({});
	let fileInput = $state<HTMLInputElement | null>(null);
	let uploading = $state(false);
	let uploadPercent = $state(0);

	async function handleUpload(event: Event) {
		const input = event.target as HTMLInputElement;
		const file = input.files?.[0];
		input.value = ''; // allow re-selecting the same file
		if (!file) return;

		if (!file.name.toLowerCase().endsWith('.zip')) {
			toast.error('Datapacks must be .zip archives containing pack.mcmeta');
			return;
		}

		uploading = true;
		uploadPercent = 0;
		try {
			const { sessionId } = await uploadFile(file, {
				onProgress: (p) => (uploadPercent = Math.round(p.percentComplete))
			});
			await rpcClient.datapack.uploadDatapack({
				serverId: server.id,
				uploadSessionId: sessionId,
				fileName: file.name
			});
			toast.success(`Uploaded datapack ${file.name}`);
			await load();
		} catch (e) {
			toast.error(e instanceof Error ? e.message : 'Upload failed');
		} finally {
			uploading = false;
		}
	}

	async function load() {
		loading = true;
		error = '';
		try {
			const res = await rpcClient.datapack.listDatapacks({ serverId: server.id });
			datapacks = (res.datapacks ?? []).map((d) => ({
				fileName: d.fileName,
				displayName: d.displayName,
				size: Number(d.size),
				packFormat: d.packFormat ?? '',
				formatWarning: d.formatWarning ?? ''
			}));
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load datapacks';
		} finally {
			loading = false;
		}
	}

	async function remove(fileName: string) {
		deleting = { ...deleting, [fileName]: true };
		try {
			await rpcClient.datapack.deleteDatapack({ serverId: server.id, fileName });
			toast.success(`Removed ${fileName}`);
			await load();
		} catch (e) {
			toast.error(e instanceof Error ? e.message : 'Delete failed');
		} finally {
			deleting = { ...deleting, [fileName]: false };
		}
	}

	$effect(() => {
		if (server.id) {
			load();
		}
	});
</script>

<div class="tab-fill flex min-h-0 flex-col overflow-hidden">
	<div class="flex shrink-0 items-center justify-between gap-4 pb-4">
		<div>
			<h3 class="flex items-center gap-2 text-lg font-semibold text-white">
				<Database class="h-5 w-5" /> Datapacks
			</h3>
			<p class="text-sm text-[#a8a8a8]">
				World data packs in <code class="font-mono">datapacks/</code>. They run on Vanilla servers
				with no mod loader.
			</p>
		</div>
		<div class="flex items-center gap-2">
			<input
				bind:this={fileInput}
				type="file"
				accept=".zip,application/zip"
				class="hidden"
				onchange={handleUpload}
			/>
			<CarbonButton kind="ghost" onclick={load} class="rounded-none text-[#a8a8a8]">
				<RefreshCw class="h-4 w-4" />
			</CarbonButton>
			<CarbonButton
				kind="ghost"
				disabled={uploading}
				onclick={() => fileInput?.click()}
				class="rounded-none text-[#c6c6c6]"
			>
				{#if uploading}<Loader2 class="h-4 w-4 animate-spin" /> Uploading… {uploadPercent}%{:else}<Upload
						class="h-4 w-4"
					/> Upload Datapack{/if}
			</CarbonButton>
			<CarbonButton
				onclick={() => (browseOpen = true)}
				class="rounded-none bg-[#0f62fe] text-white"
			>
				<Download class="h-4 w-4" /> Browse Datapacks
			</CarbonButton>
		</div>
	</div>

	<div class="min-h-0 flex-1 overflow-auto">
		{#if error}
			<p class="border border-[#da1e28]/40 bg-[#da1e28]/10 p-3 text-sm text-[#ff8389]">{error}</p>
		{/if}

		{#if loading}
			<div class="flex items-center justify-center py-8 text-[#8d8d8d]">
				<Loader2 class="h-5 w-5 animate-spin" />
			</div>
		{:else if datapacks.length === 0}
			<Card class="border-[#393939] bg-[#262626]">
				<CardContent class="flex flex-col items-center gap-2 py-10 text-center">
					<Package class="h-8 w-8 text-[#525252]" />
					<p class="text-sm text-[#8d8d8d]">No datapacks installed</p>
					<CarbonButton
						onclick={() => (browseOpen = true)}
						class="rounded-none bg-[#0f62fe] text-white"
					>
						Browse Datapacks
					</CarbonButton>
				</CardContent>
			</Card>
		{:else}
			<div class="space-y-2">
				{#each datapacks as d (d.fileName)}
					<Card class="border-[#393939] bg-[#262626]">
						<CardContent class="flex items-center gap-3 p-3">
							<Package class="h-5 w-5 shrink-0 text-[#0f62fe]" />
							<div class="min-w-0 flex-1">
								<p class="truncate font-medium whitespace-nowrap text-white" title={d.displayName}>
									{d.displayName}
								</p>
								<p class="truncate text-xs whitespace-nowrap text-[#8d8d8d]" title={d.fileName}>
									{d.fileName}
								</p>
								{#if d.packFormat}
									<p class="text-xs text-[#8d8d8d]">pack format {d.packFormat}</p>
								{/if}
								{#if d.formatWarning}
									<p class="text-xs text-[#ff8389]">{d.formatWarning}</p>
								{/if}
							</div>
							<Badge class="border-[#393939] bg-[#393939] whitespace-nowrap text-[#c6c6c6]">
								{(d.size / 1024).toFixed(1)} KB
							</Badge>
							<CarbonButton
								kind="ghost"
								onclick={() => remove(d.fileName)}
								disabled={deleting[d.fileName]}
								class="shrink-0 rounded-none text-[#ff8389] hover:bg-[#da1e28]/20"
								aria-label={`Delete ${d.displayName}`}
							>
								{#if deleting[d.fileName]}<Loader2 class="h-4 w-4 animate-spin" />{:else}<Trash2
										class="h-4 w-4"
									/>{/if}
							</CarbonButton>
						</CardContent>
					</Card>
				{/each}
			</div>
		{/if}
	</div>
</div>

<DatapackBrowserDialog
	bind:open={browseOpen}
	gameVersion={server.mcVersion ?? ''}
	install={async (datapackId, gameVersion, indexer) => {
		await rpcClient.datapack.installDatapack({
			serverId: server.id,
			datapackId,
			gameVersion,
			indexer
		});
	}}
	onInstalled={load}
/>
