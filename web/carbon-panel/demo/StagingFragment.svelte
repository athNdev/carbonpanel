<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import {
		ArrowRight,
		Check,
		Plus,
		RotateCcw,
		CheckCheck,
		LoaderCircle,
		Circle
	} from '@lucide/svelte';
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
		windowReached,
		type SampleBatch,
		type WindowSimulation
	} from './staging-model';
	import { publishDemoState } from './feedback';
	let root: HTMLDivElement;
	let ready = $state(false);
	let live = $state(initialLive());
	let draft = $state(sampleChanges.slice(0, 4).map((item) => ({ ...item })));
	let staged = $state<SampleBatch>();
	let receipt = $state<SampleBatch>();
	let applyWindow = $state('next');
	let applyAtLocal = $state('');
	let idleMinutes = $state(10);
	let applying = $state('');
	let actionError = $state('');
	const applySteps = ['Save world', 'Publish changes', 'Restart server'];
	let applyStep = $derived(
		applying === 'Saving world' ? 0 : applying === 'Publishing changes' ? 1 : 2
	);
	let simulation = $state<WindowSimulation>({
		players: 2,
		emptyMinutes: 0,
		now: 0,
		restart: false
	});
	let message = $state('Batch ready');
	let timers: ReturnType<typeof setTimeout>[] = [];
	let items = $derived(staged?.items ?? draft);
	let schedule = $derived({ mode: applyWindow, at: applyAtLocal, idle: idleMinutes });
	let error = $derived(windowError(schedule));
	let phase = $derived(
		applying ? 'applying' : staged ? 'staged' : receipt && !draft.length ? 'applied' : 'review'
	);
	const sources = [
		{ kind: 'config', label: 'Config' },
		{ kind: 'mod', label: 'Mod' },
		{ kind: 'datapack', label: 'Datapack' },
		{ kind: 'file', label: 'File' },
		{ kind: 'module', label: 'Module' },
		{ kind: 'modpack', label: 'Modpack' }
	];
	onMount(() => {
		ready = true;
		simulation.now = Date.now();
	});
	onDestroy(() => timers.forEach(clearTimeout));
	$effect(() => {
		if (ready) publishDemoState(root, 'staging', phase);
	});
	function cancelTimers() {
		timers.forEach(clearTimeout);
		timers = [];
	}
	function reset() {
		actionError = '';
		cancelTimers();
		live = initialLive();
		draft = sampleChanges.slice(0, 4).map((item) => ({ ...item }));
		staged = undefined;
		receipt = undefined;
		applying = '';
		applyWindow = 'next';
		applyAtLocal = '';
		idleMinutes = 10;
		simulation = { players: 2, emptyMinutes: 0, now: Date.now(), restart: false };
		message = 'Batch reset';
	}
	function add(kind: string) {
		const item = sampleChanges.find(
			(item) =>
				item.kind === kind &&
				!items.some((change) => change.id === item.id) &&
				live[item.id] !== item.after
		);
		if (!item || staged || applying) return;
		draft = addChange(draft, item.id, live);
		receipt = undefined;
		message = `${item.summary} added`;
	}
	function stage() {
		if (error || !draft.length) return;
		actionError = '';
		try {
			staged = stageBatch(draft, schedule);
			receipt = undefined;
			simulation = { players: 2, emptyMinutes: 0, now: Date.now(), restart: false };
			message = 'Batch staged';
		} catch (cause) {
			actionError = cause instanceof Error ? cause.message : 'Staging failed.';
		}
	}
	function revise() {
		if (!staged) return;
		draft = staged.items.map((item) => ({ ...item }));
		staged = undefined;
		message = 'Batch in review';
	}
	function discard(id: string) {
		if (applying) return;
		const remaining = id ? items.filter((item) => item.id !== id) : [];
		if (staged) staged = remaining.length ? { ...staged, items: remaining } : undefined;
		draft = remaining.map((item) => ({ ...item }));
		message = id ? 'Change discarded' : 'Batch discarded';
	}
	function apply() {
		if (!staged || applying || !windowReached(staged, simulation)) return;
		const snapshot = staged;
		applying = 'Saving world';
		message = 'Applying batch';
		timers.push(
			setTimeout(() => {
				applying = 'Publishing changes';
			}, 350)
		);
		timers.push(
			setTimeout(() => {
				applying = 'Restarting server';
			}, 700)
		);
		timers.push(
			setTimeout(() => {
				live = applyBatch(live, snapshot);
				receipt = snapshot;
				draft = [];
				staged = undefined;
				applying = '';
				message = 'Batch applied';
			}, 1100)
		);
	}
	function trigger(action: 'restart' | 'leave' | 'join' | 'minute' | 'advance') {
		if (!staged || applying) return;
		if (action === 'restart') simulation = { ...simulation, restart: true };
		if (action === 'leave') simulation = { ...simulation, players: 0, emptyMinutes: 0 };
		if (action === 'join') simulation = { ...simulation, players: 1, emptyMinutes: 0 };
		if (action === 'minute')
			simulation = {
				...simulation,
				emptyMinutes: simulation.players ? 0 : simulation.emptyMinutes + 1
			};
		if (action === 'advance')
			simulation =
				staged.window.mode === 'time'
					? { ...simulation, now: Date.parse(staged.window.at) }
					: {
							...simulation,
							emptyMinutes: simulation.players ? 0 : simulation.emptyMinutes + staged.window.idle
						};
		message = 'Window updated';
		apply();
	}
	function suggestTime() {
		const later = new Date(Date.now() + 60 * 60 * 1000);
		applyAtLocal = new Date(later.getTime() - later.getTimezoneOffset() * 60000)
			.toISOString()
			.slice(0, 16);
	}

	function windowLabel(batch: SampleBatch) {
		return batch.window.mode === 'next'
			? 'Next restart'
			: batch.window.mode === 'idle'
				? `${batch.window.idle} min idle`
				: new Date(batch.window.at).toLocaleString();
	}
</script>

<div class="fragment-root batch-demo" data-carbon-theme="dark" bind:this={root}>
	<fieldset disabled={!ready}>
		<header class="demo-heading">
			<div class="demo-identity">
				<span class="demo-eyebrow">UPDATE BATCH</span>
				<h3>Weekend survival</h3>
			</div>
			<div class="demo-heading-actions">
				<CarbonTag type={phase === 'applied' ? 'green' : phase === 'review' ? 'gray' : 'blue'}
					>{phase === 'review'
						? 'Draft'
						: phase === 'staged'
							? 'Staged'
							: phase === 'applying'
								? 'Applying'
								: 'Applied'}</CarbonTag
				><button class="demo-reset" onclick={reset} aria-label="Reset example" title="Reset example"
					><RotateCcw size={15} aria-hidden="true" /></button
				>
			</div>
		</header>
		<ol class="batch-steps" aria-label="Batch progress">
			<li
				class:current={phase === 'review'}
				class:complete={phase !== 'review'}
				aria-current={phase === 'review' ? 'step' : undefined}
			>
				<span
					>{#if phase === 'review'}01{:else}<Check size={12} aria-hidden="true" />{/if}</span
				> Review
			</li>
			<li
				class:current={phase === 'staged'}
				class:complete={phase === 'applying' || phase === 'applied'}
				aria-current={phase === 'staged' ? 'step' : undefined}
			>
				<span
					>{#if phase === 'applying' || phase === 'applied'}<Check
							size={12}
							aria-hidden="true"
						/>{:else}02{/if}</span
				> Stage
			</li>
			<li
				class:current={phase === 'applying' || phase === 'applied'}
				class:complete={phase === 'applied'}
				aria-current={phase === 'applying' || phase === 'applied' ? 'step' : undefined}
			>
				<span
					>{#if phase === 'applied'}<Check size={12} aria-hidden="true" />{:else}03{/if}</span
				> Apply
			</li>
		</ol>
		<fieldset disabled={!!applying}>
			{#if applying}
				<section class="batch-applying" aria-label="Applying batch">
					<div class="outcome-heading">
						<span class="outcome-icon"><LoaderCircle size={22} aria-hidden="true" /></span>
						<div>
							<span class="demo-eyebrow">IN PROGRESS</span>
							<h4>Applying updates</h4>
						</div>
					</div>
					<div
						role="progressbar"
						aria-label="Apply progress"
						aria-valuemin="0"
						aria-valuemax="3"
						aria-valuenow={applyStep + 1}
						class="operation-meter"
					>
						<span style={`width:${((applyStep + 1) / 3) * 100}%`}></span>
					</div>
					<ol class="operation-steps">
						{#each applySteps as label, index (label)}<li
								class:active={index === applyStep}
								class:complete={index < applyStep}
							>
								{#if index < applyStep}<Check
										size={16}
										aria-hidden="true"
									/>{:else if index === applyStep}<LoaderCircle
										size={16}
										aria-hidden="true"
									/>{:else}<Circle size={16} aria-hidden="true" />{/if}<span>{label}</span>
							</li>{/each}
					</ol>
				</section>
			{:else if receipt && phase === 'applied'}
				<section class="batch-receipt" aria-label="Applied batch result">
					<div class="outcome-heading">
						<span class="outcome-icon success"><CheckCheck size={22} aria-hidden="true" /></span>
						<div>
							<span class="demo-eyebrow">COMPLETED</span>
							<h4>Changes applied</h4>
						</div>
						<span class="outcome-count">{receipt.items.length}</span>
					</div>
					<dl class="inline-metadata">
						<div>
							<dt>Apply window</dt>
							<dd>{windowLabel(receipt)}</dd>
						</div>
					</dl>
					<table class="batch-result-table" aria-label="Applied changes">
						<thead
							><tr
								><th scope="col">Change</th><th scope="col">Previous</th><th scope="col">Current</th
								></tr
							></thead
						><tbody
							>{#each receipt.items as item (item.id)}<tr class="result-row"
									><th scope="row"
										><span class="result-kind"
											>{sources.find((source) => source.kind === item.kind)?.label}</span
										><strong>{item.summary}</strong></th
									><td class="result-before" data-label="Previous">{item.before}</td><td
										class="result-after"
										data-label="Current">{live[item.id]}</td
									></tr
								>{/each}</tbody
						>
					</table>
					<div class="demo-action-row">
						<CarbonButton
							size="sm"
							onclick={() => {
								receipt = undefined;
								message = 'New batch';
							}}>New batch<ArrowRight size={14} class="ml-3" aria-hidden="true" /></CarbonButton
						>
					</div>
				</section>
			{:else}
				{#if !staged}<section class="batch-sources" aria-label="Change sources">
						<div class="section-heading"><h4>Add changes</h4></div>
						<div class="batch-source-buttons">
							{#each sources as source (source.kind)}{@const available = sampleChanges.some(
									(item) =>
										item.kind === source.kind &&
										!items.some((change) => change.id === item.id) &&
										live[item.id] !== item.after
								)}{@const count = items.filter(
									(item) => item.kind === source.kind
								).length}<CarbonButton
									kind="secondary"
									size="sm"
									disabled={!available}
									onclick={() => add(source.kind)}
									aria-label={`Add ${source.kind} change`}
									>{#if available}<Plus size={13} aria-hidden="true" />{:else}<Check
											size={13}
											aria-hidden="true"
										/>{/if}<span class="ml-1.5">{source.label}</span>{#if count}<span
											class="source-count">{count}</span
										>{/if}</CarbonButton
								>{/each}
						</div>
					</section>{/if}
				<div class="batch-inventory">
					<UpdateBatch
						bind:applyWindow
						bind:applyAtLocal
						bind:idleMinutes
						batchOpen={items.length > 0}
						batchItems={items}
						discardStaged={discard}
						phase={staged ? 'staged' : 'draft'}
						windowLocked={!!staged}
						showHelp={false}
					>
						{#snippet actions()}{#if staged}<CarbonButton
									kind="secondary"
									size="sm"
									onclick={revise}>Revise batch</CarbonButton
								>{:else}<CarbonButton size="sm" disabled={!items.length || !!error} onclick={stage}
									>Stage {items.length || ''}
									{items.length === 1 ? 'change' : 'changes'}<ArrowRight
										size={14}
										class="ml-3"
										aria-hidden="true"
									/></CarbonButton
								>{/if}{/snippet}
					</UpdateBatch>
				</div>
				{#if !staged && applyWindow === 'time'}<div class="batch-time-help">
						<CarbonButton size="sm" kind="ghost" onclick={suggestTime}>+1 hour</CarbonButton>
					</div>{/if}
				{#if (error || actionError) && !staged}<p class="batch-error" role="alert">
						{actionError || error}
					</p>{/if}
				{#if staged}<section class="batch-simulation" aria-label="Simulate the apply window">
						<div class="section-heading">
							<div>
								<span class="demo-eyebrow">DEMO CONTROLS</span>
								<h4>
									{staged.window.mode === 'next'
										? 'Restart trigger'
										: staged.window.mode === 'idle'
											? 'Idle trigger'
											: 'Scheduled trigger'}
								</h4>
							</div>
						</div>
						{#if staged.window.mode === 'next'}<div class="demo-action-row">
								<CarbonButton size="sm" onclick={() => trigger('restart')}
									>Simulate restart<ArrowRight
										size={14}
										class="ml-3"
										aria-hidden="true"
									/></CarbonButton
								>
							</div>
						{:else if staged.window.mode === 'idle'}<dl class="demo-metrics">
								<div>
									<dt>Players online</dt>
									<dd>{simulation.players}</dd>
								</div>
								<div>
									<dt>Idle time</dt>
									<dd>{simulation.emptyMinutes}<small>/ {staged.window.idle} min</small></dd>
								</div>
							</dl>
							<progress
								class="idle-meter"
								max={staged.window.idle}
								value={simulation.emptyMinutes}
								aria-label="Idle time"
							></progress>
							<div class="simulation-controls">
								<div class="simulation-player-actions">
									<CarbonButton
										size="sm"
										kind="secondary"
										disabled={simulation.players === 0}
										onclick={() => trigger('leave')}>Simulate players leaving</CarbonButton
									><CarbonButton
										size="sm"
										kind="secondary"
										disabled={simulation.players > 0}
										onclick={() => trigger('join')}>Simulate player joining</CarbonButton
									>
								</div>
								<div class="demo-action-row">
									<CarbonButton
										size="sm"
										kind="ghost"
										disabled={simulation.players > 0}
										onclick={() => trigger('minute')}>Advance 1 empty minute</CarbonButton
									><CarbonButton
										size="sm"
										disabled={simulation.players > 0}
										onclick={() => trigger('advance')}
										>Advance {staged.window.idle} empty minutes</CarbonButton
									>
								</div>
							</div>
						{:else}<dl class="inline-metadata">
								<div>
									<dt>Scheduled time</dt>
									<dd>{windowLabel(staged)}</dd>
								</div>
							</dl>
							<div class="demo-action-row">
								<CarbonButton size="sm" onclick={() => trigger('advance')}
									>Advance to scheduled time<ArrowRight
										size={14}
										class="ml-3"
										aria-hidden="true"
									/></CarbonButton
								>
							</div>{/if}
					</section>{/if}
				<details class="batch-live">
					<summary>Live configuration <span>Read only</span></summary>
					<dl>
						{#each sampleChanges as item (item.id)}<div>
								<dt>{item.summary}</dt>
								<dd>{live[item.id]}</dd>
							</div>{/each}
					</dl>
				</details>
			{/if}
		</fieldset>
		<div class="sr-only" role="status">{applying || message}</div>
	</fieldset>
</div>
