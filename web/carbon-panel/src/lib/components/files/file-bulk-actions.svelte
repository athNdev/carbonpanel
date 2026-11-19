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
	import { Download, Trash2, FolderInput, Archive, Package, X } from '@lucide/svelte';

	interface Props {
		selectedCount: number;
		canExtract: boolean;
		onClear: () => void;
		onDelete: () => void;
		onDownload: () => void;
		onMove: () => void;
		onCompress: () => void;
		onExtract: () => void;
	}

	let {
		selectedCount,
		canExtract,
		onClear,
		onDelete,
		onDownload,
		onMove,
		onCompress,
		onExtract
	}: Props = $props();

	let active = $derived(selectedCount > 0);
</script>

<!-- Always rendered at fixed height to prevent layout shift -->
<div
	class="flex h-[36px] items-center justify-between rounded-none border-b border-border px-3 font-sans transition-colors {active
		? 'bg-card text-foreground'
		: 'bg-background text-muted-foreground'}"
>
	{#if active}
		<div class="flex items-center gap-2">
			<span class="font-mono text-xs font-medium text-accent-text">{selectedCount} selected</span>
			<button
				type="button"
				class="flex h-6 cursor-pointer items-center gap-1 rounded-none bg-secondary px-2 text-xs text-foreground hover:bg-secondary"
				onclick={onClear}
			>
				<X class="h-3 w-3" />
				<span>Clear</span>
			</button>
		</div>
		<div class="flex items-center gap-1">
			<button
				type="button"
				class="flex h-7 w-7 cursor-pointer items-center justify-center rounded-none text-muted-foreground hover:bg-secondary hover:text-foreground"
				onclick={onDownload}
				title="Download selected"
				aria-label="Download selected"
			>
				<Download class="h-3.5 w-3.5" />
			</button>
			<button
				type="button"
				class="flex h-7 w-7 cursor-pointer items-center justify-center rounded-none text-muted-foreground hover:bg-secondary hover:text-foreground"
				onclick={onMove}
				title="Move selected"
				aria-label="Move selected"
			>
				<FolderInput class="h-3.5 w-3.5" />
			</button>
			<button
				type="button"
				class="flex h-7 w-7 cursor-pointer items-center justify-center rounded-none text-muted-foreground hover:bg-secondary hover:text-foreground"
				onclick={onCompress}
				title="Compress selected"
				aria-label="Compress selected"
			>
				<Archive class="h-3.5 w-3.5" />
			</button>
			{#if canExtract}
				<button
					type="button"
					class="flex h-7 w-7 cursor-pointer items-center justify-center rounded-none text-muted-foreground hover:bg-secondary hover:text-foreground"
					onclick={onExtract}
					title="Extract archive"
					aria-label="Extract archive"
				>
					<Package class="h-3.5 w-3.5" />
				</button>
			{/if}
			<button
				type="button"
				class="flex h-7 w-7 cursor-pointer items-center justify-center rounded-none text-destructive-text hover:bg-destructive/20"
				onclick={onDelete}
				title="Delete selected"
				aria-label="Delete selected"
			>
				<Trash2 class="h-3.5 w-3.5" />
			</button>
		</div>
	{:else}
		<span class="font-mono text-xs text-muted-foreground"
			>Ctrl+Click to select • Right-click for file actions</span
		>
	{/if}
</div>
