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
	let {
		applyWindow = $bindable('next'),
		applyAtLocal = $bindable(''),
		idleMinutes = $bindable(10),
		batchOpen,
		batchItems,
		discardStaged
	}: {
		applyWindow?: string;
		applyAtLocal?: string;
		idleMinutes?: number;
		batchOpen: boolean;
		batchItems: Array<{ id: string; summary: string }>;
		discardStaged: (itemId: string) => void;
	} = $props();
</script>

<div class="space-y-3 rounded-none border-t border-[#393939] bg-[#262626] p-4">
	<div class="flex flex-wrap items-end gap-3">
		<label class="flex flex-col gap-1 font-mono text-xs text-[#c6c6c6]">
			Apply window
			<select
				bind:value={applyWindow}
				class="h-8 border border-[#393939] bg-[#161616] px-2 text-[#f4f4f4]"
			>
				<option value="next">On next restart</option>
				<option value="time">At a time</option>
				<option value="idle">After idle</option>
			</select>
		</label>
		{#if applyWindow === 'time'}
			<label class="flex flex-col gap-1 font-mono text-xs text-[#c6c6c6]">
				UTC is stored from this local time
				<input
					type="datetime-local"
					bind:value={applyAtLocal}
					class="h-8 border border-[#393939] bg-[#161616] px-2 text-[#f4f4f4]"
				/>
			</label>
		{/if}
		{#if applyWindow === 'idle'}
			<label class="flex flex-col gap-1 font-mono text-xs text-[#c6c6c6]">
				Minutes with nobody online
				<input
					type="number"
					min="1"
					value={idleMinutes}
					oninput={(event) => {
						const target = event.currentTarget as HTMLInputElement;
						idleMinutes = Number(target.value) || 0;
					}}
					class="h-8 w-24 border border-[#393939] bg-[#161616] px-2 text-[#f4f4f4]"
				/>
			</label>
		{/if}
	</div>
	{#if batchOpen}
		<ul class="space-y-1">
			{#each batchItems as item (item.id)}
				<li class="flex items-center justify-between gap-3 font-mono text-xs text-[#f4f4f4]">
					<span>{item.summary}</span>
					<button
						type="button"
						onclick={() => discardStaged(item.id)}
						class="h-7 cursor-pointer border border-[#393939] px-2 text-[#c6c6c6] hover:text-white"
					>
						Discard
					</button>
				</li>
			{/each}
		</ul>
		<button
			type="button"
			onclick={() => discardStaged('')}
			class="h-8 cursor-pointer border border-[#393939] px-3 font-mono text-xs text-[#c6c6c6] hover:text-white"
		>
			Discard batch
		</button>
	{/if}
	<p class="font-mono text-xs text-[#c6c6c6]">
		Staged changes leave the live files and server state untouched.
		{#if applyWindow === 'time'}
			Apply at the chosen time.
		{:else if applyWindow === 'idle'}
			Apply after {idleMinutes} empty minutes. A player joining resets the wait.
		{:else}
			Apply on the next restart.
		{/if}
		Timed and idle batches restart running or frozen servers and keep deep sleep stopped.
		Apply now restarts immediately.
	</p>
</div>
