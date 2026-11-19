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
		Breadcrumb,
		BreadcrumbItem,
		BreadcrumbLink,
		BreadcrumbList,
		BreadcrumbPage,
		BreadcrumbSeparator
	} from '$lib/components/ui/breadcrumb';
	import { FolderRoot } from '@lucide/svelte';

	interface Props {
		currentPath: string;
		onNavigate: (path: string) => void;
	}

	let { currentPath, onNavigate }: Props = $props();

	let segments = $derived.by(() => {
		if (!currentPath) return [];
		return currentPath.split('/').filter(Boolean);
	});
</script>

<div
	class="flex items-center border-b border-border bg-background px-3 py-1.5 font-mono text-xs text-muted-foreground"
>
	<Breadcrumb>
		<BreadcrumbList>
			<BreadcrumbItem>
				{#if segments.length === 0}
					<BreadcrumbPage class="flex items-center gap-1.5 text-xs font-medium text-foreground">
						<FolderRoot class="h-3.5 w-3.5 text-accent-text" />
						<span>/ (root)</span>
					</BreadcrumbPage>
				{:else}
					<BreadcrumbLink
						class="flex cursor-pointer items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-accent-text"
						onclick={() => onNavigate('')}
					>
						<FolderRoot class="h-3.5 w-3.5 text-accent-text" />
						<span>/ (root)</span>
					</BreadcrumbLink>
				{/if}
			</BreadcrumbItem>
			{#each segments as segment, i (i)}
				<BreadcrumbSeparator class="text-muted-foreground" />
				<BreadcrumbItem>
					{#if i === segments.length - 1}
						<BreadcrumbPage class="text-xs font-medium text-foreground">{segment}</BreadcrumbPage>
					{:else}
						<BreadcrumbLink
							class="cursor-pointer text-xs text-muted-foreground transition-colors hover:text-accent-text"
							onclick={() => onNavigate(segments.slice(0, i + 1).join('/'))}
						>
							{segment}
						</BreadcrumbLink>
					{/if}
				</BreadcrumbItem>
			{/each}
		</BreadcrumbList>
	</Breadcrumb>
</div>
