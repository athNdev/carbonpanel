<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->
<script module lang="ts">
	export interface StudioMod {
		slug: string;
		name: string;
		file_name: string;
		side: 'both' | 'client' | 'server';
		platform: string;
		pinned?: boolean;
	}
</script>

<script lang="ts" generics="T extends StudioMod">
	import { Package, Pin, PinOff, Trash2 } from '@lucide/svelte';
	import CarbonButton from '$lib/components/carbon/CarbonButton.svelte';
	import CarbonTag from '$lib/components/carbon/CarbonTag.svelte';
	let {
		filteredMods,
		selectedSlugs = $bindable([]),
		updatesMap = {},
		toggleSide,
		togglePin,
		removeMod
	}: {
		filteredMods: T[];
		selectedSlugs?: string[];
		updatesMap?: Record<string, { update_available?: boolean }>;
		toggleSide: (mod: T) => void;
		togglePin: (mod: T) => void;
		removeMod: (mod: T) => void;
	} = $props();
</script>

<div class="studio-mod-table overflow-x-auto" data-testid="showcase-mods-table">
	<table class="w-full border-collapse text-left text-xs" aria-label="Mods in this pack">
		<thead
			class="border-b border-[#525252] bg-[#393939] font-semibold tracking-wider text-[#f4f4f4] uppercase"
		>
			<tr>
				<th scope="col" class="w-8 px-3 py-2.5">
					<input
						type="checkbox"
						aria-label="Select all mods"
						class="cursor-pointer rounded-none border-[#8d8d8d] bg-[#262626]"
						checked={filteredMods.length > 0 &&
							filteredMods.every((mod) => selectedSlugs.includes(mod.slug))}
						onchange={(e) => {
							const checked = (e.target as HTMLInputElement).checked;
							const visible = new Set(filteredMods.map((mod) => mod.slug));
							selectedSlugs = checked
								? [...new Set([...selectedSlugs, ...visible])]
								: selectedSlugs.filter((slug) => !visible.has(slug));
						}}
					/>
				</th>
				<th scope="col" class="px-3 py-2.5">Mod Name</th>
				<th scope="col" class="detail px-3 py-2.5">File Name</th>
				<th scope="col" class="detail px-3 py-2.5">Platform</th>
				<th scope="col" class="px-3 py-2.5">Side</th>
				<th scope="col" class="px-3 py-2.5">Pin</th>
				<th scope="col" class="detail px-3 py-2.5">Status</th>
				<th scope="col" class="px-3 py-2.5 text-right">Actions</th>
			</tr>
		</thead>
		<tbody class="divide-y divide-[#393939] bg-[#262626] text-[#f4f4f4]">
			{#each filteredMods as mod (mod.slug)}
				<tr
					class="transition-colors hover:bg-[#353535] {selectedSlugs.includes(mod.slug)
						? 'bg-[#0f62fe]/10'
						: ''}"
				>
					<td class="px-3 py-3">
						<input
							type="checkbox"
							aria-label={`Select ${mod.slug}`}
							class="cursor-pointer rounded-none border-[#8d8d8d] bg-[#262626]"
							checked={selectedSlugs.includes(mod.slug)}
							onchange={(e) => {
								const checked = (e.target as HTMLInputElement).checked;
								if (checked) {
									selectedSlugs = [...selectedSlugs, mod.slug];
								} else {
									selectedSlugs = selectedSlugs.filter((s) => s !== mod.slug);
								}
							}}
						/>
					</td>
					<td class="px-3 py-3 font-semibold text-white">
						<div class="flex items-center gap-2">
							<Package class="h-4 w-4 shrink-0 text-accent-text" />
							<span>{mod.name}</span>
						</div>
					</td>
					<td class="detail max-w-[180px] truncate px-3 py-3 font-mono text-xs text-[#a8a8a8]">
						{mod.file_name}
					</td>
					<td class="px-3 py-3">
						<CarbonTag type="cyan" size="sm" class="uppercase">
							{mod.platform}
						</CarbonTag>
					</td>
					<td class="px-3 py-3">
						<CarbonButton
							size="sm"
							kind="ghost"
							class="rounded-none"
							onclick={() => toggleSide(mod)}
							title="Click to toggle side (Both -> Server -> Client)"
							aria-label={`Toggle side for ${mod.name} (currently ${mod.side})`}
						>
							<CarbonTag
								type={mod.side === 'both' ? 'blue' : mod.side === 'server' ? 'green' : 'purple'}
								size="sm"
							>
								{mod.side.toUpperCase()}
							</CarbonTag>
						</CarbonButton>
					</td>
					<td class="px-3 py-3">
						<button
							type="button"
							onclick={() => togglePin(mod)}
							class="cursor-pointer rounded-none p-1 text-[#8d8d8d] transition-colors hover:text-white"
							title={mod.pinned ? 'Version pinned' : 'Click to pin version'}
							aria-label={mod.pinned
								? `Unpin version for ${mod.name}`
								: `Pin version for ${mod.name}`}
							aria-pressed={!!mod.pinned}
						>
							{#if mod.pinned}
								<Pin class="h-3.5 w-3.5 text-accent-text" />
							{:else}
								<PinOff class="h-3.5 w-3.5 opacity-40 hover:opacity-100" />
							{/if}
						</button>
					</td>
					<td class="px-3 py-3">
						{#if updatesMap[mod.slug]?.update_available}
							<CarbonTag type="magenta" size="sm">Update Ready</CarbonTag>
						{:else}
							<span class="font-mono text-xs text-[#8d8d8d]">Up to date</span>
						{/if}
					</td>
					<td class="px-3 py-3 text-right">
						<CarbonButton
							kind="ghost"
							size="sm"
							iconOnly
							class="rounded-none text-[#da1e28] hover:bg-[#da1e28]/20"
							onclick={() => removeMod(mod)}
							title="Remove mod"
							aria-label={`Remove ${mod.name}`}
						>
							<Trash2 class="h-3.5 w-3.5" />
						</CarbonButton>
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>

<style>
	.studio-mod-table {
		container-type: inline-size;
		scrollbar-gutter: stable;
	}
	table {
		min-width: 320px;
	}
	@container (max-width: 800px) {
		th:nth-child(3),
		th:nth-child(4),
		th:nth-child(7),
		td:nth-child(3),
		td:nth-child(4),
		td:nth-child(7) {
			display: none;
		}
	}
	@container (max-width: 420px) {
		table {
			min-width: 0;
		}
		thead {
			display: none;
		}
		tbody,
		tr {
			display: block;
		}
		tr {
			display: grid;
			grid-template-columns: 20px minmax(0, 1fr) 32px 32px;
			gap: 2px 6px;
			padding: 12px;
		}
		td {
			padding: 0;
			min-width: 0;
		}
		td:first-child {
			grid-column: 1;
			grid-row: 1 / 3;
			align-self: center;
		}
		td:nth-child(2) {
			grid-column: 2 / 5;
			grid-row: 1;
			padding: 2px 0 6px;
		}
		td:nth-child(5) {
			grid-column: 2;
			grid-row: 2;
		}
		td:nth-child(6) {
			grid-column: 3;
			grid-row: 2;
		}
		td:nth-child(8) {
			grid-column: 4;
			grid-row: 2;
		}
		td:nth-child(5) :global(button) {
			padding-inline: 0;
		}
		td:nth-child(6) :global(button) {
			min-width: 32px;
			min-height: 32px;
		}
	}
</style>
