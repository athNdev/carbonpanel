<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<!--
	Presentational preview of a validated server archive. All labels and
	calculations come from archive-presentation.ts; this component only renders.
	No runtime imports so the rendered output can be verified in isolation.
-->
<script lang="ts">
	import type { ArchivePreviewView } from './archive-presentation';

	interface Props {
		view: ArchivePreviewView;
		nodeId?: string;
		port?: number;
	}

	let { view, nodeId = '', port = 0 }: Props = $props();
</script>

<div class="space-y-4 rounded-none border border-border bg-card p-4" data-testid="archive-preview">
	<div class="flex flex-wrap items-center justify-between gap-2">
		<h3 class="text-sm font-semibold text-foreground">Archive preview</h3>
		<span class="border border-border bg-background px-2 py-0.5 font-mono text-xs text-foreground">
			{view.format}
		</span>
	</div>

	<p class="font-sans text-xs text-muted-foreground">
		Schema {view.schemaVersion} · exporter {view.exporterVersion || 'unspecified'} · {view.fileCount}
		files · {view.expandedBytes} expanded
	</p>

	<div
		class="grid grid-cols-2 gap-x-4 gap-y-2 border-y border-border py-3 font-mono text-xs sm:grid-cols-3"
	>
		<div>
			<span class="block text-muted-foreground">Minecraft</span>
			<span class="text-foreground">{view.mcVersion || 'unspecified'}</span>
		</div>
		<div>
			<span class="block text-muted-foreground">Mod loader</span>
			<span class="text-foreground">{view.modLoader}</span>
		</div>
		<div>
			<span class="block text-muted-foreground">Loader version</span>
			<span class="text-foreground">{view.loaderVersion || 'default'}</span>
		</div>
		<div>
			<span class="block text-muted-foreground">Memory</span>
			<span class="text-foreground">{view.memoryMb} MB</span>
		</div>
		<div>
			<span class="block text-muted-foreground">Max players</span>
			<span class="text-foreground">{view.maxPlayers}</span>
		</div>
	</div>

	<div class="space-y-1 font-mono text-xs">
		<div class="flex flex-wrap items-baseline gap-x-6 gap-y-1">
			<span class="inline-flex items-baseline gap-1.5">
				<span class="text-muted-foreground">Required</span>
				<span class="text-foreground">{view.requiredBytes}</span>
			</span>
			<span class="inline-flex items-baseline gap-1.5">
				<span class="text-muted-foreground">Available</span>
				<span class="text-foreground">{view.availableBytes ?? 'not checked yet'}</span>
			</span>
		</div>
		<p
			class="font-sans {view.capacity === 'insufficient'
				? 'text-destructive-text'
				: view.capacity === 'sufficient'
					? 'text-success-text'
					: 'text-muted-foreground'}"
		>
			{view.capacityText}
		</p>
	</div>

	<div class="space-y-1 border-t border-border pt-3 font-mono text-xs">
		<div class="flex flex-wrap items-baseline gap-x-6 gap-y-1">
			<span class="inline-flex items-baseline gap-1.5">
				<span class="text-muted-foreground">Destination</span>
				<span class="text-foreground">{nodeId || 'automatic placement'}</span>
			</span>
			<span class="inline-flex items-baseline gap-1.5">
				<span class="text-muted-foreground">Game port</span>
				<span class="text-foreground">{port > 0 ? port : 'assign automatically'}</span>
			</span>
		</div>
	</div>

	{#if view.isBlocked}
		<p
			role="alert"
			class="border-l-4 border-destructive bg-destructive/10 p-3 font-sans text-xs text-destructive-text"
			data-testid="archive-preview-blocked"
		>
			This archive has blocking findings. Resolve them before creating a server from it.
		</p>
	{/if}

	{#if view.findings.length > 0}
		<ul class="divide-y divide-border border border-border" data-testid="archive-preview-findings">
			{#each view.findings as finding, index (finding.code + index)}
				<li class="flex flex-wrap items-start gap-2 p-3">
					<span
						class="shrink-0 px-1.5 py-0.5 font-mono text-[10px] uppercase {finding.isBlocker
							? 'bg-destructive/20 text-destructive-text'
							: 'bg-warning/20 text-warning-text'}"
					>
						{finding.severity}
					</span>
					<span class="shrink-0 font-mono text-[10px] text-muted-foreground uppercase">
						{finding.code}
					</span>
					<span class="min-w-0 flex-1 font-sans text-xs text-foreground">{finding.message}</span>
				</li>
			{/each}
		</ul>
	{:else}
		<p class="font-sans text-xs text-muted-foreground">No compatibility findings.</p>
	{/if}
</div>
