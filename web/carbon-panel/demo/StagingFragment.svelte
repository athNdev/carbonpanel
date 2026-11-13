<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { ArrowRight, Check, Plus, RotateCcw, CheckCheck } from '@lucide/svelte';
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
	let simulation = $state<WindowSimulation>({
		players: 2,
		emptyMinutes: 0,
		now: 0,
		restart: false
	});
	let message = $state(
		'Four edits are ready to review. Add more from across the panel, then stage them together.'
	);
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
		message = 'Sample restored. Review four changes without touching the live server.';
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
		message = `${item.summary} added to review. Live values have not changed.`;
	}
	function stage() {
		if (error || !draft.length) return;
		try {
			staged = stageBatch(draft, schedule);
			receipt = undefined;
			simulation = { players: 2, emptyMinutes: 0, now: Date.now(), restart: false };
			message = `${staged.items.length} changes staged. Reproduce the chosen window below to see them go live.`;
		} catch (cause) {
			message = cause instanceof Error ? cause.message : 'Review the apply window.';
		}
	}
	function revise() {
		if (!staged) return;
		draft = staged.items.map((item) => ({ ...item }));
		staged = undefined;
		message = 'Back in review. The live server still has its original values.';
	}
	function discard(id: string) {
		if (applying) return;
		const remaining = id ? items.filter((item) => item.id !== id) : [];
		if (staged) staged = remaining.length ? { ...staged, items: remaining } : undefined;
		draft = remaining.map((item) => ({ ...item }));
		message = `${id ? 'Change removed' : 'Batch discarded'}. Live values remain unchanged.`;
	}
	function apply() {
		if (!staged || applying || !windowReached(staged, simulation)) return;
		const snapshot = staged;
		applying = 'Saving the world';
		message = 'Window reached. Saving the sample world before one restart.';
		timers.push(
			setTimeout(() => {
				applying = 'Publishing the batch';
			}, 350)
		);
		timers.push(
			setTimeout(() => {
				applying = 'Starting the server';
			}, 700)
		);
		timers.push(
			setTimeout(() => {
				live = applyBatch(live, snapshot);
				receipt = snapshot;
				draft = [];
				staged = undefined;
				applying = '';
				message = `${snapshot.items.length} ${snapshot.items.length === 1 ? 'change' : 'changes'} applied with one simulated restart. Review the exact result below.`;
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
		message = simulation.players
			? 'A sample player is online. The idle wait resets.'
			: 'The sample server is empty. Advance the idle wait to apply the batch.';
		apply();
	}
	function suggestTime() {
		const later = new Date(Date.now() + 60 * 60 * 1000);
		applyAtLocal = new Date(later.getTime() - later.getTimezoneOffset() * 60000)
			.toISOString()
			.slice(0, 16);
	}
</script>

<div class="fragment-root" data-carbon-theme="dark" bind:this={root}>
	<fieldset disabled={!ready}>
		<div class="demo-heading">
			<div>
				<h3>Weekend survival</h3>
				<p>ONE SERVER · ONE UPDATE BATCH</p>
			</div>
			<CarbonTag type={phase === 'applied' ? 'green' : phase === 'review' ? 'gray' : 'blue'}
				>{phase === 'review'
					? 'In review'
					: phase === 'staged'
						? 'Waiting'
						: phase === 'applying'
							? 'Applying'
							: 'Applied'}</CarbonTag
			>
		</div>
		<ol class="batch-steps" aria-label="Batch progress">
			<li class:current={phase === 'review'}><span>01</span> Collect</li>
			<li class:current={phase === 'staged'}><span>02</span> Stage</li>
			<li class:current={phase === 'applying' || phase === 'applied'}>
				<span>03</span> Apply together
			</li>
		</ol>
		<fieldset disabled={!!applying}>
			{#if phase !== 'applied'}
				{#if !staged}<div class="batch-sources">
						<p>Add edits from across the panel</p>
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
									>{#if available}<Plus size={12} aria-hidden="true" />{:else}<Check
											size={12}
											aria-hidden="true"
										/>{/if}<span class="ml-1.5">{source.label}{count ? ` ${count}` : ''}</span
									></CarbonButton
								>{/each}
						</div>
					</div>{/if}
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
						<CarbonButton size="sm" kind="ghost" onclick={suggestTime}
							>Use one hour from now</CarbonButton
						>
					</div>{/if}
				{#if error && !staged}<p class="batch-error" role="alert">{error}</p>{/if}
				{#if staged}<section class="batch-simulation" aria-label="Simulate the apply window">
						<div>
							<h4>{applying || 'Try the apply window'}</h4>
							<p>
								Browser-only simulation · live values stay unchanged until the window is reached.
							</p>
						</div>
						{#if staged.window.mode === 'next'}<p>
								The batch waits for a restart. It will publish all {staged.items.length} changes in that
								restart.
							</p>
							<CarbonButton size="sm" onclick={() => trigger('restart')}
								>Simulate restart</CarbonButton
							>
						{:else if staged.window.mode === 'idle'}<p>
								<strong>{simulation.players} online</strong> · {simulation.emptyMinutes} / {staged
									.window.idle} empty minutes
							</p>
							<div class="batch-source-buttons">
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
								><CarbonButton
									size="sm"
									kind="secondary"
									disabled={simulation.players > 0}
									onclick={() => trigger('minute')}>Advance 1 empty minute</CarbonButton
								><CarbonButton
									size="sm"
									disabled={simulation.players > 0}
									onclick={() => trigger('advance')}
									>Advance {staged.window.idle} empty minutes</CarbonButton
								>
							</div>
						{:else}<p>
								Scheduled for {new Date(staged.window.at).toLocaleString()}. No real timer is
								running.
							</p>
							<CarbonButton size="sm" onclick={() => trigger('advance')}
								>Advance to scheduled time</CarbonButton
							>{/if}
						{#if applying}<ol class="apply-progress" aria-label="Apply progress">
								<li>Save world</li>
								<li>Publish {staged.items.length} changes</li>
								<li>Restart once</li>
							</ol>{/if}
					</section>{/if}
			{:else if receipt}
				<section class="batch-receipt" aria-label="Applied batch result">
					<CheckCheck size={24} strokeWidth={1.5} aria-hidden="true" />
					<h4>Everything landed together.</h4>
					<p>
						{receipt.items.length}
						{receipt.items.length === 1 ? 'change' : 'changes'} published · one simulated restart · server
						running
					</p>
					<p>
						Window reached: {receipt.window.mode === 'next'
							? 'next restart'
							: receipt.window.mode === 'idle'
								? `${receipt.window.idle} empty minutes`
								: new Date(receipt.window.at).toLocaleString()}.
					</p>
					<ul>
						{#each receipt.items as item (item.id)}<li>
								<strong>{item.summary}</strong><span
									><s>{item.before}</s><ArrowRight size={12} aria-hidden="true" /><b
										>{live[item.id]}</b
									></span
								>
							</li>{/each}
					</ul>
					<CarbonButton
						size="sm"
						kind="secondary"
						onclick={() => {
							receipt = undefined;
							message = 'Previous changes are live. Collect the remaining edits for another batch.';
						}}>Prepare another batch</CarbonButton
					>
				</section>
			{/if}
		</fieldset>
		<div class="demo-result" role="status">{message}</div>
		{#if phase !== 'applied'}<details class="batch-live">
				<summary
					>Inspect live server <span
						>{receipt ? 'Previous batch applied' : 'Not changed by this batch'}</span
					></summary
				>
				<dl>
					{#each sampleChanges as item (item.id)}<div>
							<dt>{item.summary}</dt>
							<dd>{live[item.id]}</dd>
						</div>{/each}
				</dl>
			</details>{/if}
		<div class="demo-footer">
			<span>Sample data · timing is simulated</span><button class="demo-reset" onclick={reset}
				><RotateCcw size={12} aria-hidden="true" /> Reset example</button
			>
		</div>
	</fieldset>
</div>
