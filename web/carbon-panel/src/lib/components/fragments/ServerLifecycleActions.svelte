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
	import { Play, Square, RotateCw, Loader2 } from '@lucide/svelte';
	import { ServerStatus } from '$lib/proto/carbonpanel/v1/common_pb';
	let {
		status,
		hasContainer = true,
		actionLoading = false,
		onaction
	}: {
		status: ServerStatus;
		hasContainer?: boolean;
		actionLoading?: boolean;
		onaction: (action: 'start' | 'stop' | 'restart') => void;
	} = $props();
</script>

<div class="flex flex-wrap items-center gap-2" data-component="server-lifecycle-actions">
	{#if status === ServerStatus.CREATING}
		<div
			class="flex h-10 items-center gap-2 rounded-none border border-[#393939] bg-[#262626] px-4 font-sans text-sm text-[#f4f4f4] select-none"
		>
			<Loader2 class="h-4 w-4 animate-spin text-accent-text" />
			<span>Creating Server...</span>
		</div>
	{:else if status === ServerStatus.DEEPSLEEP}
		<button
			type="button"
			onclick={() => onaction('start')}
			disabled={actionLoading}
			class="flex h-10 cursor-pointer items-center gap-2 rounded-none bg-[#8a3ffc] px-4 font-sans text-sm text-white transition-colors hover:bg-[#a56eff] disabled:opacity-50"
		>
			{#if actionLoading}
				<Loader2 class="h-4 w-4 animate-spin" />
			{:else}
				<Play class="h-4 w-4 fill-current" />
			{/if}
			<span>Wake Server</span>
		</button>
		<button
			type="button"
			onclick={() => onaction('stop')}
			disabled={actionLoading}
			class="flex h-10 cursor-pointer items-center gap-2 rounded-none bg-[#da1e28] px-4 font-sans text-sm text-white transition-colors hover:bg-[#ba1b23] disabled:opacity-50"
		>
			<Square class="h-4 w-4 fill-current" />
			<span>Stop</span>
		</button>
	{:else if status === ServerStatus.PAUSED}
		<button
			type="button"
			onclick={() => onaction('start')}
			disabled={actionLoading}
			class="flex h-10 cursor-pointer items-center gap-2 rounded-none bg-[#0f62fe] px-4 font-sans text-sm text-white transition-colors hover:bg-[#4589ff] disabled:opacity-50"
		>
			{#if actionLoading}
				<Loader2 class="h-4 w-4 animate-spin" />
			{:else}
				<Play class="h-4 w-4 fill-current" />
			{/if}
			<span>Resume Server</span>
		</button>
		<button
			type="button"
			onclick={() => onaction('stop')}
			disabled={actionLoading}
			class="flex h-10 cursor-pointer items-center gap-2 rounded-none bg-[#da1e28] px-4 font-sans text-sm text-white transition-colors hover:bg-[#ba1b23] disabled:opacity-50"
		>
			<Square class="h-4 w-4 fill-current" />
			<span>Stop</span>
		</button>
	{:else if status === ServerStatus.STOPPED || !hasContainer}
		<button
			type="button"
			onclick={() => onaction('start')}
			disabled={actionLoading}
			class="flex h-10 cursor-pointer items-center gap-2 rounded-none bg-[#198038] px-4 font-sans text-sm text-white transition-colors hover:bg-[#24a148] disabled:opacity-50"
		>
			{#if actionLoading}
				<Loader2 class="h-4 w-4 animate-spin" />
			{:else}
				<Play class="h-4 w-4 fill-current" />
			{/if}
			<span>Start Server</span>
		</button>
	{:else if status === ServerStatus.ERROR}
		<button
			type="button"
			onclick={() => onaction('restart')}
			disabled={actionLoading}
			class="flex h-10 cursor-pointer items-center gap-2 rounded-none bg-[#f1c21b] px-4 font-sans text-sm font-medium text-black transition-colors hover:bg-[#d2a106] disabled:opacity-50"
		>
			{#if actionLoading}
				<Loader2 class="h-4 w-4 animate-spin" />
			{:else}
				<RotateCw class="h-4 w-4" />
			{/if}
			<span>Restart</span>
		</button>
		<button
			type="button"
			onclick={() => onaction('stop')}
			disabled={actionLoading}
			class="flex h-10 cursor-pointer items-center gap-2 rounded-none bg-[#da1e28] px-4 font-sans text-sm text-white transition-colors hover:bg-[#ba1b23] disabled:opacity-50"
		>
			<Square class="h-4 w-4 fill-current" />
			<span>Stop</span>
		</button>
	{:else if status === ServerStatus.RUNNING || status === ServerStatus.STARTING || status === ServerStatus.UNHEALTHY}
		<button
			type="button"
			onclick={() => onaction('stop')}
			disabled={actionLoading}
			class="flex h-10 cursor-pointer items-center gap-2 rounded-none bg-[#da1e28] px-4 font-sans text-sm text-white transition-colors hover:bg-[#ba1b23] disabled:opacity-50"
		>
			{#if actionLoading}
				<Loader2 class="h-4 w-4 animate-spin" />
			{:else}
				<Square class="h-4 w-4 fill-current" />
			{/if}
			<span>Stop</span>
		</button>
		<button
			type="button"
			onclick={() => onaction('restart')}
			disabled={actionLoading}
			class="flex h-10 cursor-pointer items-center gap-2 rounded-none border border-[#525252] bg-[#393939] px-4 font-sans text-sm text-white transition-colors hover:bg-[#4c4c4c] disabled:opacity-50"
		>
			<RotateCw class="h-4 w-4" />
			<span>Restart</span>
		</button>
	{:else if status === ServerStatus.STOPPING}
		<div
			class="flex h-10 items-center gap-2 rounded-none border border-[#525252] bg-[#393939] px-4 font-sans text-sm text-[#c6c6c6]"
		>
			<Loader2 class="h-4 w-4 animate-spin" />
			<span>Stopping...</span>
		</div>
	{/if}
</div>
