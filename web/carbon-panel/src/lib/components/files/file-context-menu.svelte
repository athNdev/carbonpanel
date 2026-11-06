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
	import {
		FileEdit,
		Pencil,
		Copy,
		FolderInput,
		Download,
		FilePlus,
		FolderPlus,
		Upload,
		Archive,
		Package,
		Trash2
	} from '@lucide/svelte';

	interface Props {
		visible: boolean;
		x: number;
		y: number;
		file: FileInfo | null;
		hasSelection: boolean;
		selectedCount: number;
		onClose: () => void;
		onEdit: () => void;
		onRename: () => void;
		onCopy: () => void;
		onMove: () => void;
		onDownload: () => void;
		onNewFile: () => void;
		onNewFolder: () => void;
		onUpload: () => void;
		onCompress: () => void;
		onExtract: () => void;
		onDelete: () => void;
	}

	let {
		visible,
		x,
		y,
		file,
		hasSelection,
		selectedCount,
		onClose,
		onEdit,
		onRename,
		onCopy,
		onMove,
		onDownload,
		onNewFile,
		onNewFolder,
		onUpload,
		onCompress,
		onExtract,
		onDelete
	}: Props = $props();

	function isArchive(f: FileInfo | null): boolean {
		if (!f || f.isDir) return false;
		const ext = f.name.toLowerCase().split('.').pop() || '';
		return [
			'zip',
			'tar',
			'gz',
			'tgz',
			'rar',
			'7z',
			'bz2',
			'xz',
			'lz',
			'zst',
			'tbz2',
			'txz'
		].includes(ext);
	}

	function handleAction(action: () => void) {
		action();
		onClose();
	}

	// Adjust position so menu doesn't go off-screen
	let menuStyle = $derived.by(() => {
		const maxX = typeof window !== 'undefined' ? window.innerWidth - 200 : x;
		const maxY = typeof window !== 'undefined' ? window.innerHeight - 300 : y;
		return `left: ${Math.min(x, maxX)}px; top: ${Math.min(y, maxY)}px;`;
	});

	const itemClass =
		'flex items-center gap-2 w-full px-3 py-1.5 text-left font-sans text-xs text-[#f4f4f4] cursor-pointer rounded-none hover:bg-[#353535] hover:text-white transition-colors';
	const dangerClass =
		'flex items-center gap-2 w-full px-3 py-1.5 text-left font-sans text-xs text-[#ff8389] cursor-pointer rounded-none hover:bg-[#da1e28]/20 transition-colors';
</script>

{#if visible}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div
		class="fixed inset-0 z-50"
		onclick={onClose}
		oncontextmenu={(e) => {
			e.preventDefault();
			onClose();
		}}
	>
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="fixed z-50 min-w-[180px] rounded-none border border-[#393939] bg-[#161616] py-1 text-[#f4f4f4] shadow-lg"
			style={menuStyle}
			onclick={(e) => e.stopPropagation()}
		>
			{#if file?.isEditable}
				<button class={itemClass} onclick={() => handleAction(onEdit)}>
					<FileEdit class="h-3.5 w-3.5" />
					Edit
				</button>
			{/if}

			{#if file}
				<button class={itemClass} onclick={() => handleAction(onRename)}>
					<Pencil class="h-3.5 w-3.5" />
					Rename
				</button>
			{/if}

			<button class={itemClass} onclick={() => handleAction(onCopy)}>
				<Copy class="h-3.5 w-3.5" />
				Copy{hasSelection && selectedCount > 1 ? ` (${selectedCount})` : ''}
			</button>

			<button class={itemClass} onclick={() => handleAction(onMove)}>
				<FolderInput class="h-3.5 w-3.5" />
				Move to...{hasSelection && selectedCount > 1 ? ` (${selectedCount})` : ''}
			</button>

			<button class={itemClass} onclick={() => handleAction(onDownload)}>
				<Download class="h-3.5 w-3.5" />
				Download{hasSelection && selectedCount > 1 ? ` (${selectedCount})` : ''}
			</button>

			<div class="mx-2 my-1 border-t border-[#393939]"></div>

			{#if file?.isDir}
				<button class={itemClass} onclick={() => handleAction(onNewFile)}>
					<FilePlus class="h-3.5 w-3.5" />
					New File
				</button>
				<button class={itemClass} onclick={() => handleAction(onNewFolder)}>
					<FolderPlus class="h-3.5 w-3.5" />
					New Folder
				</button>
				<button class={itemClass} onclick={() => handleAction(onUpload)}>
					<Upload class="h-3.5 w-3.5" />
					Upload Here
				</button>
				<div class="mx-2 my-1 border-t border-[#393939]"></div>
			{/if}

			<button class={itemClass} onclick={() => handleAction(onCompress)}>
				<Archive class="h-3.5 w-3.5" />
				Compress{hasSelection && selectedCount > 1 ? ` (${selectedCount})` : ''}
			</button>

			{#if isArchive(file)}
				<button class={itemClass} onclick={() => handleAction(onExtract)}>
					<Package class="h-3.5 w-3.5" />
					Extract
				</button>
			{/if}

			<div class="mx-2 my-1 border-t border-[#393939]"></div>

			<button class={dangerClass} onclick={() => handleAction(onDelete)}>
				<Trash2 class="h-3.5 w-3.5" />
				Delete{hasSelection && selectedCount > 1 ? ` (${selectedCount})` : ''}
			</button>
		</div>
	</div>
{/if}
