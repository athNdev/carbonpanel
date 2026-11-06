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
	import type { FileInfo } from '$lib/proto/carbonpanel/v1/file_pb';
	import { Checkbox } from '$lib/components/ui/checkbox';
	import FileTreeRow from './file-tree-row.svelte';

	interface Props {
		flatFiles: FileInfo[];
		emptyLabel?: string;
		expandedDirs: Set<string>;
		selectedPaths: Set<string>;
		focusedPath: string;
		dragOverPath: string;
		onToggleExpand: (path: string) => void;
		onSelect: (file: FileInfo, event: MouseEvent) => void;
		onCheckboxToggle: (file: FileInfo) => void;
		onSelectAll: () => void;
		onContextMenu: (file: FileInfo, event: MouseEvent) => void;
		onDragStart: (file: FileInfo, event: DragEvent) => void;
		onDragOver: (file: FileInfo, event: DragEvent) => void;
		onDragLeave: () => void;
		onDrop: (file: FileInfo, event: DragEvent) => void;
		onKeydown: (event: KeyboardEvent) => void;
		getDepth: (file: FileInfo) => number;
	}

	let {
		flatFiles,
		emptyLabel = 'This folder is empty',
		expandedDirs,
		selectedPaths,
		focusedPath,
		dragOverPath,
		onToggleExpand,
		onSelect,
		onCheckboxToggle,
		onSelectAll,
		onContextMenu,
		onDragStart,
		onDragOver,
		onDragLeave,
		onDrop,
		onKeydown,
		getDepth
	}: Props = $props();

	let hasSelection = $derived(selectedPaths.size > 0);
	let allSelected = $derived(flatFiles.length > 0 && selectedPaths.size === flatFiles.length);

	let container: HTMLDivElement | undefined = $state();

	// Arrow-key navigation moves focus through the flat list; keep the focused
	// row visible when the list scrolls past the viewport.
	$effect(() => {
		if (!focusedPath || !container) return;
		const row = container.querySelector(`[data-path="${CSS.escape(focusedPath)}"]`);
		row?.scrollIntoView({ block: 'nearest' });
	});
</script>

<div
	bind:this={container}
	class="min-h-0 flex-1 overflow-auto focus:outline-none focus-visible:ring-1 focus-visible:ring-[#0f62fe] focus-visible:ring-inset"
	tabindex="0"
	role="tree"
	onkeydown={onKeydown}
>
	<!-- Column header - matches row layout exactly -->
	<div
		class="sticky top-0 z-10 flex h-[26px] items-center border-b bg-background pr-3 text-xs text-muted-foreground"
	>
		<!-- Checkbox column -->
		<div
			class="flex w-6 shrink-0 items-center justify-center {hasSelection ? 'visible' : 'invisible'}"
		>
			<Checkbox checked={allSelected} onCheckedChange={onSelectAll} class="h-3.5 w-3.5" />
		</div>
		<!-- Indent placeholder + chevron column -->
		<div class="w-4 shrink-0"></div>
		<!-- Name -->
		<div class="flex-1 pl-1 font-medium tracking-wider uppercase">Name</div>
		<!-- Size -->
		<span class="w-20 shrink-0 text-right font-medium tracking-wider uppercase">Size</span>
		<!-- Modified -->
		<span
			class="hidden w-24 shrink-0 text-right font-medium tracking-wider uppercase sm:inline-block"
			>Modified</span
		>
	</div>

	<!-- File rows -->
	{#each flatFiles as file (file.path)}
		<FileTreeRow
			{file}
			depth={getDepth(file)}
			isExpanded={expandedDirs.has(file.path)}
			isSelected={selectedPaths.has(file.path)}
			isFocused={focusedPath === file.path}
			isDragOver={dragOverPath === file.path}
			{hasSelection}
			{onToggleExpand}
			{onSelect}
			{onCheckboxToggle}
			{onContextMenu}
			{onDragStart}
			{onDragOver}
			{onDragLeave}
			{onDrop}
		/>
	{/each}

	{#if flatFiles.length === 0}
		<div class="flex flex-col items-center justify-center py-12 text-sm text-muted-foreground">
			<p>{emptyLabel}</p>
		</div>
	{/if}
</div>
