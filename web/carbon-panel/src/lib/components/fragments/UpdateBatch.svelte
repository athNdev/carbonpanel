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
	import type { Snippet } from 'svelte';
	import { ArrowRight, Box, Clock3, FileText, Layers, Package, Settings2, X } from '@lucide/svelte';
	import CarbonButton from '../carbon/CarbonButton.svelte';
	let {
		applyWindow = $bindable('next'),
		applyAtLocal = $bindable(''),
		idleMinutes = $bindable(10),
		batchOpen,
		batchItems,
		discardStaged,
		phase = 'staged',
		windowLocked = false,
		showHelp = true,
		actions
	}: {
		applyWindow?: string;
		applyAtLocal?: string;
		idleMinutes?: number;
		batchOpen: boolean;
		batchItems: Array<{
			id: string;
			summary: string;
			kind?: string;
			action?: string;
			before?: string;
			after?: string;
		}>;
		discardStaged: (itemId: string) => void;
		phase?: 'draft' | 'staged';
		windowLocked?: boolean;
		showHelp?: boolean;
		actions?: Snippet;
	} = $props();
	const labels: Record<string, string> = {
		config: 'Configuration',
		mod: 'Mod',
		datapack: 'Datapack',
		file: 'File',
		modpack: 'Modpack',
		module: 'Module'
	};
	const icons: Record<string, typeof Settings2> = {
		config: Settings2,
		mod: Box,
		datapack: Layers,
		file: FileText,
		modpack: Package,
		module: Box
	};
	let kinds = $derived(new Set(batchItems.map((item) => item.kind).filter(Boolean)).size);
	const windowId = $props.id();
</script>

<section
	class="update-batch"
	aria-label={phase === 'draft' ? 'Review changes' : 'Staged update batch'}
>
	<header class="inventory-heading">
		<div>
			<h4>{phase === 'draft' ? 'Review changes' : 'Staged changes'}</h4>
			<p>
				{batchItems.length}
				{batchItems.length === 1 ? 'change' : 'changes'}{kinds > 1 ? ` across ${kinds} areas` : ''}
			</p>
		</div>
	</header>
	{#if batchOpen && batchItems.length}
		<ul class="inventory-list">
			{#each batchItems as item (item.id)}
				{@const Icon = icons[item.kind ?? ''] ?? FileText}
				<li>
					<span class="item-icon"><Icon size={16} strokeWidth={1.5} aria-hidden="true" /></span>
					<div class="item-content">
						<div class="item-meta">
							<span>{labels[item.kind ?? ''] ?? item.kind ?? 'Change'}</span>{#if item.action}<span
									>{item.action}</span
								>{/if}
						</div>
						<p class="item-title">{item.summary}</p>
						{#if item.before !== undefined && item.after !== undefined}
							<div class="item-diff">
								<span><span class="sr-only">Current: </span>{item.before}</span><ArrowRight
									size={12}
									aria-hidden="true"
								/><strong><span class="sr-only">Proposed: </span>{item.after}</strong>
							</div>
						{/if}
					</div>
					<CarbonButton
						kind="ghost"
						size="sm"
						iconOnly
						onclick={() => discardStaged(item.id)}
						aria-label={`${phase === 'draft' ? 'Remove' : 'Discard'} ${item.summary}`}
						title={phase === 'draft' ? 'Remove change' : 'Discard change'}
						><X size={14} aria-hidden="true" /></CarbonButton
					>
				</li>
			{/each}
		</ul>
	{:else}
		<p class="inventory-empty">
			{phase === 'draft' ? 'No changes in this batch.' : 'No staged changes.'}
		</p>
	{/if}
	<div class="batch-window">
		{#if windowLocked}
			<div class="locked-window">
				<Clock3 size={16} aria-hidden="true" />
				<dl>
					<dt>Apply window</dt>
					<dd>
						{applyWindow === 'next'
							? 'Next restart'
							: applyWindow === 'idle'
								? `${idleMinutes} min idle`
								: new Date(applyAtLocal).toLocaleString()}
					</dd>
				</dl>
			</div>
		{:else}
			<fieldset disabled={windowLocked}>
				<legend><Clock3 size={13} aria-hidden="true" /> Apply window</legend>
				<div class="window-choices">
					{#each [{ value: 'next', label: 'Next restart' }, { value: 'idle', label: 'When empty' }, { value: 'time', label: 'Pick a time' }] as option (option.value)}
						<label class:selected={applyWindow === option.value}
							><input
								type="radio"
								name={windowId}
								value={option.value}
								bind:group={applyWindow}
							/><span>{option.label}</span></label
						>
					{/each}
				</div>
				{#if applyWindow === 'time'}
					<label class="window-input"
						>Date and time <span class="window-hint">Local time, saved as UTC</span><input
							type="datetime-local"
							bind:value={applyAtLocal}
						/></label
					>
				{:else if applyWindow === 'idle'}
					<label class="window-input"
						>Minutes with nobody online<input
							type="number"
							min="1"
							step="1"
							bind:value={idleMinutes}
						/></label
					>
				{/if}
			</fieldset>
		{/if}
		{#if showHelp}
			<p class="window-explanation">
				{#if applyWindow === 'idle'}A player joining resets the wait. A running server restarts
					once.{:else if applyWindow === 'time'}A running server restarts once at the chosen time.{:else}All
					changes land together with the server’s next restart.{/if}
			</p>
			<details class="batch-safety">
				<summary>What happens to the live server?</summary>
				<p>
					Staging leaves live files and server state unchanged. Timed and idle batches save and
					restart a running server once; a frozen server restarts and stays running. Stopped and
					deep-sleeping servers stay stopped. If publishing fails, the previous files and
					configuration are restored and the batch stays open. Apply now restarts a running server
					immediately.
				</p>
			</details>
		{/if}
	</div>
	{#if batchOpen || actions}
		<footer class="inventory-actions">
			{#if batchOpen}<CarbonButton kind="ghost" size="sm" onclick={() => discardStaged('')}
					>{phase === 'draft' ? 'Clear batch' : 'Discard batch'}</CarbonButton
				>{/if}
			{#if actions}<div class="primary-actions">{@render actions()}</div>{/if}
		</footer>
	{/if}
</section>

<style>
	.update-batch {
		color: var(--foreground);
		background: var(--background);
		min-width: 0;
	}
	.inventory-heading {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 16px;
		padding: 20px 22px 14px;
	}
	.inventory-heading h4 {
		margin: 0;
		font-size: 16px;
		font-weight: 500;
	}
	.inventory-heading p {
		margin: 4px 0 0;
		font-size: 12px;
		color: var(--muted-foreground);
	}
	.locked-window {
		display: flex;
		align-items: center;
		gap: 12px;
	}
	.locked-window dl {
		display: flex;
		flex: 1;
		align-items: center;
		justify-content: space-between;
		gap: 16px;
		margin: 0;
	}
	.locked-window dt {
		font-size: 12px;
		color: var(--muted-foreground);
	}
	.locked-window dd {
		margin: 0;
		font-size: 13px;
		font-weight: 500;
	}
	.inventory-list {
		list-style: none;
		margin: 0;
		padding: 0 22px;
	}
	.inventory-list li {
		display: flex;
		gap: 12px;
		align-items: center;
		padding: 14px 0;
		border-top: 1px solid var(--border);
	}
	.item-icon {
		display: grid;
		place-items: center;
		width: 32px;
		height: 36px;
		flex-shrink: 0;
		background: var(--card);
		color: var(--muted-foreground);
	}
	.item-content {
		flex: 1;
		min-width: 0;
	}
	.item-meta {
		display: flex;
		align-items: center;
		gap: 9px;
		font: 9px/1.5 var(--carbon-font-mono);
		color: var(--muted-foreground);
		text-transform: uppercase;
	}
	.item-meta span + span::before {
		content: '·';
		margin-right: 9px;
	}
	.item-title {
		font-size: 14px;
		font-weight: 500;
		line-height: 1.5;
		margin: 3px 0;
		overflow-wrap: anywhere;
	}
	.item-diff {
		display: flex;
		gap: 8px;
		align-items: center;
		flex-wrap: wrap;
		font: 12px/1.5 var(--carbon-font-mono);
		color: var(--muted-foreground);
	}
	.item-diff strong {
		font-weight: 400;
		color: var(--accent-text);
	}
	.inventory-empty {
		padding: 12px 22px 24px;
		margin: 0;
		font-size: 12px;
		color: var(--muted-foreground);
	}
	.batch-window {
		padding: 18px 22px;
		background: var(--card);
		border-block: 1px solid var(--border);
		margin-top: 6px;
	}
	fieldset {
		border: 0;
		padding: 0;
		margin: 0;
		min-width: 0;
	}
	legend {
		display: flex;
		align-items: center;
		gap: 7px;
		padding: 0;
		margin-bottom: 12px;
		font-size: 11px;
		font-weight: 500;
	}
	.window-choices {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 5px;
	}
	.window-choices label {
		position: relative;
		display: flex;
		justify-content: center;
		align-items: center;
		padding: 10px 4px;
		cursor: pointer;
		font-size: 11px;
		border: 1px solid var(--border);
		color: var(--muted-foreground);
		background: var(--background);
		min-width: 0;
	}
	.window-choices .selected {
		color: var(--foreground);
		border-color: var(--accent-text);
		box-shadow: inset 0 -2px var(--accent-text);
	}
	.window-choices input {
		position: absolute;
		opacity: 0;
		width: 1px;
		height: 1px;
	}
	.window-choices label:has(input:focus-visible) {
		outline: 2px solid var(--ring);
		outline-offset: 3px;
	}
	fieldset:disabled .window-choices label {
		cursor: default;
	}
	.window-input {
		display: grid;
		gap: 6px;
		margin-top: 14px;
		font-size: 11px;
	}
	.window-hint {
		color: var(--muted-foreground);
	}
	.window-input input {
		min-width: 0;
		max-width: 100%;
		width: 100%;
		height: 36px;
		background: var(--background);
		color: var(--foreground);
		border: 0;
		border-bottom: 1px solid var(--input);
		padding: 0 10px;
		font: 12px var(--carbon-font-mono);
		border-radius: 0;
		color-scheme: inherit;
	}
	.window-input input[type='number'] {
		width: 92px;
	}
	.window-explanation {
		font-size: 11px;
		line-height: 1.6;
		color: var(--muted-foreground);
		margin: 12px 0 0;
	}
	.batch-safety {
		font-size: 10px;
		line-height: 1.6;
		color: var(--muted-foreground);
		margin-top: 9px;
	}
	.batch-safety summary {
		cursor: pointer;
		text-underline-offset: 3px;
	}
	.batch-safety p {
		margin: 8px 0 0;
	}
	.inventory-actions {
		display: flex;
		justify-content: space-between;
		flex-wrap: wrap;
		align-items: center;
		gap: 10px;
		padding: 16px 22px;
	}
	.primary-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
		margin-left: auto;
	}
	@media (max-width: 500px) {
		.inventory-heading,
		.inventory-actions,
		.batch-window {
			padding-inline: 16px;
		}
		.inventory-list {
			padding-inline: 16px;
		}
		.inventory-list li {
			gap: 10px;
		}
		.item-icon {
			width: 26px;
		}
		.item-diff {
			font-size: 10px;
			gap: 5px;
		}
		.window-choices label {
			font-size: 10px;
		}
	}
</style>
