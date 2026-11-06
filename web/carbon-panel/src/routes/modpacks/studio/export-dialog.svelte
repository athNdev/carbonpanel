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
		CarbonTag,
		CarbonTextInput,
		CarbonSelect
	} from '$lib/components/carbon';
	import { Download, Loader2, TriangleAlert } from '@lucide/svelte';
	import { toast } from 'svelte-sonner';
	import {
		EXPORT_FORMATS,
		DEFAULT_EXPORT_OPTIONS,
		downloadPackExport,
		findCurseForgeDroppedMods,
		formatExportLabel,
		type PackExportFormat,
		type PackExportOptions
	} from './pack-export';

	interface Props {
		open: boolean;
		packId: string;
		packName: string;
		mods?: {
			name: string;
			platform: string;
			project_id?: string;
			version_id?: string;
			side?: string;
		}[];
	}

	let { open = $bindable(false), packId, packName, mods = [] }: Props = $props();

	let format = $state<PackExportFormat>('mrpack');
	let includeClient = $state(DEFAULT_EXPORT_OPTIONS.includeClient);
	let optionalMode = $state<PackExportOptions['optionalMode']>('keep');
	let overrides = $state<PackExportOptions['overrides']>('all');
	let summary = $state('');
	let exporting = $state(false);
	let droppedError = $state<string[]>([]);

	let cfPreview = $derived(format === 'curseforge' ? findCurseForgeDroppedMods(mods) : []);

	async function runExport() {
		if (!packId) return;
		exporting = true;
		droppedError = [];
		try {
			const result = await downloadPackExport(packId, packName, format, {
				includeClient,
				optionalMode,
				overrides,
				summary
			});
			if (result.ok) {
				toast.success(`Exported ${formatExportLabel(format)}`);
				open = false;
			} else {
				if (result.dropped) droppedError = result.dropped;
				toast.error(result.message);
			}
		} catch (err) {
			toast.error((err instanceof Error ? err.message : undefined) || 'Export failed');
		} finally {
			exporting = false;
		}
	}
</script>

<CarbonModal
	bind:open
	title="Export Modpack"
	description="Choose a launcher format and per-launcher options."
	size="lg"
	hasFooter={false}
>
	<div class="space-y-4">
		<div class="space-y-2">
			<span class="text-xs font-semibold tracking-wide uppercase">Format</span>
			<div
				class="grid grid-cols-1 gap-2 sm:grid-cols-2"
				role="radiogroup"
				aria-label="Export format"
			>
				{#each EXPORT_FORMATS as f (f.id)}
					<button
						type="button"
						role="radio"
						aria-checked={format === f.id}
						onclick={() => (format = f.id)}
						class="cursor-pointer rounded-none border p-3 text-left transition-colors {format ===
						f.id
							? 'border-primary bg-primary/10'
							: 'border-border hover:bg-muted/40'}"
					>
						<span class="flex items-center gap-2 text-sm font-semibold">
							<span
								class="inline-block h-2.5 w-2.5 rounded-full border {format === f.id
									? 'border-primary bg-primary'
									: 'border-muted-foreground'}"
								aria-hidden="true"
							></span>
							{f.label}
						</span>
						<span class="mt-1 block text-xs text-muted-foreground">{f.hint}</span>
					</button>
				{/each}
			</div>
		</div>

		<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
			<label class="flex cursor-pointer items-start gap-2.5">
				<input type="checkbox" bind:checked={includeClient} class="mt-0.5 cursor-pointer" />
				<span>
					<span class="block text-xs font-semibold">Include client-only mods</span>
					<span class="block text-xs text-muted-foreground">
						Turn off for a server-only pack.
					</span>
				</span>
			</label>

			<CarbonSelect label="Optional handling" bind:value={optionalMode}>
				<option value="keep">Keep required flags</option>
				<option value="required">Force all required</option>
				<option value="optional">Mark all optional</option>
			</CarbonSelect>
		</div>

		<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
			<CarbonSelect label="Overrides" bind:value={overrides}>
				<option value="all">All overrides</option>
				<option value="configs-only">Configs only</option>
				<option value="none">No overrides</option>
			</CarbonSelect>

			<CarbonTextInput
				label="Summary override (optional)"
				placeholder="Short launcher description"
				bind:value={summary}
			/>
		</div>

		{#if format === 'curseforge'}
			<div class="space-y-2 rounded-none border border-border bg-muted p-3" role="alert">
				<div class="flex items-center gap-2 text-sm font-semibold">
					<TriangleAlert class="h-4 w-4" />
					CurseForge can drop items
					<CarbonTag type="yellow" size="sm">Data-loss warning</CarbonTag>
				</div>
				{#if cfPreview.length > 0}
					<p class="text-xs text-muted-foreground">
						{cfPreview.length} item(s) in this pack have no CurseForge project/file ID and will block
						the export:
					</p>
					<ul class="max-h-24 list-disc overflow-y-auto pl-5 font-mono text-xs">
						{#each cfPreview.slice(0, 20) as name (name)}
							<li>{name}</li>
						{/each}
					</ul>
					{#if cfPreview.length > 20}
						<p class="font-mono text-xs text-muted-foreground">
							…and {cfPreview.length - 20} more
						</p>
					{/if}
				{:else}
					<p class="text-xs text-muted-foreground">
						Every mod in this pack carries a CurseForge project/file ID, so the export should
						complete without drops. Modrinth and direct-URL mods block the export instead of
						shipping a partial pack.
					</p>
				{/if}
				{#if droppedError.length > 0}
					<p class="text-xs font-semibold">Blocked by the server:</p>
					<ul class="max-h-24 list-disc overflow-y-auto pl-5 font-mono text-xs">
						{#each droppedError as name (name)}
							<li>{name}</li>
						{/each}
					</ul>
				{/if}
			</div>
		{/if}

		<div class="flex items-center justify-end gap-3 border-t border-border pt-4">
			<CarbonButton kind="secondary" onclick={() => (open = false)} disabled={exporting}>
				Cancel
			</CarbonButton>
			<CarbonButton kind="primary" onclick={runExport} disabled={exporting}>
				{#if exporting}
					<Loader2 class="mr-2 h-4 w-4 animate-spin" />
					Exporting…
				{:else}
					<Download class="mr-2 h-4 w-4" />
					Export {formatExportLabel(format)}
				{/if}
			</CarbonButton>
		</div>
	</div>
</CarbonModal>
