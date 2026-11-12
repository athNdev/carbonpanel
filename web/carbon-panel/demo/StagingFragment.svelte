<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { ArrowRight, Check, Plus, RotateCcw } from '@lucide/svelte';
	import UpdateBatch from '$lib/components/fragments/UpdateBatch.svelte';
	import CarbonButton from '$lib/components/carbon/CarbonButton.svelte';
	import CarbonTag from '$lib/components/carbon/CarbonTag.svelte';
	import {
		sampleChanges,
		initialLive,
		addChange,
		windowError,
		stageBatch,
		applyBatch,
		type SampleBatch
	} from './staging-model';
	let ready = $state(false);
	let live = $state(initialLive());
	let draft = $state(sampleChanges.slice(0, 3).map((item) => ({ ...item })));
	let staged = $state<SampleBatch | undefined>();
	let applyWindow = $state('next');
	let applyAtLocal = $state('');
	let idleMinutes = $state(10);
	let applied = $state(false);
	let showLive = $state(false);
	let message = $state(
		'Three sample changes, one batch. Add another or remove anything you don’t need.'
	);
	let items = $derived(staged?.items ?? draft);
	let schedule = $derived({ mode: applyWindow, at: applyAtLocal, idle: idleMinutes });
	let error = $derived(windowError(schedule));
	let when = $derived(
		applyWindow === 'idle'
			? `after ${idleMinutes} empty minutes`
			: applyWindow === 'time' && applyAtLocal
				? `at ${new Date(applyAtLocal).toLocaleString()}`
				: 'on the next restart'
	);
	const sourceNames: Record<string, string> = {
		config: 'Config',
		mod: 'Mod',
		datapack: 'Datapack',
		file: 'File',
		module: 'Module',
		modpack: 'Modpack'
	};
	onMount(() => {
		ready = true;
	});
	function reset() {
		live = initialLive();
		draft = sampleChanges.slice(0, 3).map((item) => ({ ...item }));
		staged = undefined;
		applyWindow = 'next';
		applyAtLocal = '';
		idleMinutes = 10;
		applied = false;
		showLive = false;
		message = 'Sample restored. Review changes from across the panel in one place.';
	}
	function add(id: string) {
		if (staged) return;
		draft = addChange(draft, id, live);
		applied = false;
		message = 'Added to your review. The live sample is unchanged.';
	}
	function stage() {
		if (error || !draft.length) return;
		try {
			staged = stageBatch(draft, schedule);
			message = `${staged.items.length} changes staged ${when}. The live sample is unchanged.`;
		} catch (cause) {
			message = cause instanceof Error ? cause.message : 'Review the apply window and try again.';
		}
	}
	function revise() {
		if (!staged) return;
		draft = staged.items.map((item) => ({ ...item }));
		staged = undefined;
		message = 'Back in review. Stage the batch again when your changes are ready.';
	}
	function apply() {
		if (!staged) return;
		const count = staged.items.length;
		live = applyBatch(live, staged);
		draft = [];
		staged = undefined;
		applied = true;
		message = `${count} changes applied together. One restart simulated. Open the live sample to see the result.`;
	}
	function discard(id: string) {
		if (staged) {
			const remaining = id ? staged.items.filter((item) => item.id !== id) : [];
			staged = remaining.length ? { ...staged, items: remaining } : undefined;
			draft = remaining.map((item) => ({ ...item }));
		} else draft = id ? draft.filter((item) => item.id !== id) : [];
		message = `${id ? 'Change removed' : 'Batch cleared'}. The live sample is unchanged.`;
	}
</script>

<div class="fragment-root" data-carbon-theme="dark">
	<fieldset disabled={!ready}>
		<div class="demo-heading">
			<div>
				<h3>Weekend survival</h3>
				<p>ONE SERVER · ONE UPDATE BATCH</p>
			</div>
			<CarbonTag type={staged ? 'blue' : applied ? 'green' : 'gray'}
				>{staged ? 'Staged' : applied ? 'Applied' : 'In review'}</CarbonTag
			>
		</div>
		<ol class="batch-steps" aria-label="Batch progress">
			<li class:current={!staged && !applied}><span>01</span> Collect</li>
			<li class:current={!!staged}><span>02</span> Stage</li>
			<li class:current={applied}><span>03</span> Apply together</li>
		</ol>
		{#if !staged}
			<div class="batch-sources">
				<p>Add a sample change from</p>
				<div class="batch-source-buttons">
					{#each sampleChanges as item (item.id)}
						{@const included = items.some((change) => change.id === item.id)}
						<CarbonButton
							kind="secondary"
							size="sm"
							disabled={included || live[item.id] === item.after}
							onclick={() => add(item.id)}
							aria-label={`Add ${sourceNames[item.kind].toLowerCase()} change`}
						>
							{#if included}<Check size={12} aria-hidden="true" />{:else}<Plus
									size={12}
									aria-hidden="true"
								/>{/if}
							<span class="ml-1.5">{sourceNames[item.kind]}</span>
						</CarbonButton>
					{/each}
				</div>
			</div>
		{/if}
		<UpdateBatch
			bind:applyWindow
			bind:applyAtLocal
			bind:idleMinutes
			batchOpen={items.length > 0}
			batchItems={items}
			discardStaged={discard}
			phase={staged ? 'staged' : 'draft'}
			windowLocked={!!staged}
		>
			{#snippet actions()}
				{#if staged}
					<CarbonButton kind="secondary" size="sm" onclick={revise}>Revise batch</CarbonButton>
					<CarbonButton size="sm" onclick={apply}
						>Simulate apply <ArrowRight size={14} class="ml-3" aria-hidden="true" /></CarbonButton
					>
				{:else}
					<CarbonButton size="sm" disabled={!items.length || !!error} onclick={stage}
						>Stage {items.length || ''}
						{items.length === 1 ? 'change' : 'changes'}
						<ArrowRight size={14} class="ml-3" aria-hidden="true" /></CarbonButton
					>
				{/if}
			{/snippet}
		</UpdateBatch>
		{#if error && !staged}<p class="batch-error" role="alert">{error}</p>{/if}
		<div class="demo-result" role="status">{message}</div>
		<details class="batch-live" bind:open={showLive}>
			<summary>Live sample <span>{applied ? 'Updated together' : 'Unchanged'}</span></summary>
			<dl>
				{#each sampleChanges as item (item.id)}<div>
						<dt>{item.summary}</dt>
						<dd>{live[item.id]}</dd>
					</div>{/each}
			</dl>
		</details>
		<div class="batch-footnote">
			<span>Sample data. Timing is simulated.</span><button class="demo-reset" onclick={reset}
				><RotateCcw size={12} aria-hidden="true" /> Reset example</button
			>
		</div>
	</fieldset>
</div>

<style>
	.batch-steps {
		display: flex;
		justify-content: space-between;
		gap: 10px;
		list-style: none;
		padding: 14px 22px;
		margin: 0;
		border-bottom: 1px solid var(--border);
		font: 10px/1.5 var(--carbon-font-mono);
		color: var(--muted-foreground);
	}
	.batch-steps li {
		display: flex;
		gap: 7px;
		align-items: center;
	}
	.batch-steps span {
		opacity: 0.65;
	}
	.batch-steps .current {
		color: var(--accent-text);
	}
	.batch-sources {
		padding: 16px 22px;
		border-bottom: 1px solid var(--border);
	}
	.batch-sources p {
		margin: 0 0 10px;
		font-size: 11px;
		color: var(--muted-foreground);
	}
	.batch-source-buttons {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.batch-error {
		color: var(--destructive-text);
		font-size: 12px;
		padding: 0 22px 12px;
	}
	.batch-live {
		border-top: 1px solid var(--border);
		padding: 0 22px;
		font-size: 11px;
	}
	.batch-live summary {
		cursor: pointer;
		padding: 12px 0;
	}
	.batch-live summary span {
		color: var(--muted-foreground);
		margin-left: 8px;
	}
	.batch-live dl {
		margin: 0;
		padding-bottom: 12px;
	}
	.batch-live dl div {
		display: flex;
		justify-content: space-between;
		gap: 16px;
		padding: 5px 0;
	}
	.batch-live dt {
		color: var(--muted-foreground);
		overflow-wrap: anywhere;
	}
	.batch-live dd {
		margin: 0;
		text-align: right;
	}
	.batch-footnote {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 4px 10px;
		padding: 8px 18px 8px 22px;
		border-top: 1px solid var(--border);
		color: var(--muted-foreground);
		font-size: 10px;
	}
	.batch-footnote button {
		display: inline-flex;
		align-items: center;
		gap: 6px;
	}
	@media (max-width: 400px) {
		.batch-steps,
		.batch-sources,
		.batch-live {
			padding-inline: 16px;
		}
		.batch-steps {
			font-size: 9px;
		}
		.batch-steps li {
			gap: 4px;
		}
		.batch-footnote {
			padding-inline: 16px;
		}
	}
</style>
