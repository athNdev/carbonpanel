<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { onMount } from 'svelte';
	import UpdateBatch from '$lib/components/fragments/UpdateBatch.svelte';
	import CarbonButton from '$lib/components/carbon/CarbonButton.svelte';
	import CarbonTag from '$lib/components/carbon/CarbonTag.svelte';
	let ready = $state(false);
	let liveDistance = $state(10);
	let distance = $state(12);
	let applyWindow = $state('next');
	let applyAtLocal = $state('');
	let idleMinutes = $state(10);
	let stagedDistance = $state<number | undefined>();
	let batchItems = $derived(
		stagedDistance === undefined
			? []
			: [{ id: 'view-distance', summary: `View distance: ${liveDistance} → ${stagedDistance}` }]
	);
	let message = $state('Prepare a change. The live sample stays at 10 chunks until you apply it.');
	let valid = $derived(Number.isInteger(distance) && distance >= 2 && distance <= 32);
	onMount(() => {
		ready = true;
	});
	function reset() {
		liveDistance = 10;
		distance = 12;
		stagedDistance = undefined;
		applyWindow = 'next';
		applyAtLocal = '';
		idleMinutes = 10;
		message = 'Sample restored. Live view distance is 10 chunks.';
	}
	function stage() {
		if (!valid) return;
		if (applyWindow === 'time' && !Number.isFinite(Date.parse(applyAtLocal))) {
			message = 'Choose a date and time before staging.';
			return;
		}
		if (applyWindow === 'idle' && (!Number.isInteger(idleMinutes) || idleMinutes < 1)) {
			message = 'Choose at least one whole idle minute.';
			return;
		}
		stagedDistance = distance;
		const window =
			applyWindow === 'time'
				? `at ${new Date(applyAtLocal).toISOString()}`
				: applyWindow === 'idle'
					? `after ${idleMinutes} empty minutes`
					: 'on the next restart';
		message = `Staged ${window}. Live sample remains at ${liveDistance} chunks. Timing is illustrative; use Apply now to simulate publishing.`;
	}
	function apply() {
		if (!valid) return;
		liveDistance = distance;
		stagedDistance = undefined;
		message = `Simulated apply and restart complete. Live sample is now ${liveDistance} chunks.`;
	}
	function discardStaged() {
		stagedDistance = undefined;
		message = `Batch discarded. Live sample is still ${liveDistance} chunks.`;
	}
</script>

<div class="fragment-root" data-carbon-theme="dark">
	<fieldset disabled={!ready}>
		<div class="demo-heading">
			<div>
				<h3>Weekend survival</h3>
				<p>CONFIGURATION · LIVE: {liveDistance} CHUNKS</p>
			</div>
			<CarbonTag type={batchItems.length ? 'blue' : 'gray'}>{batchItems.length} staged</CarbonTag>
		</div>
		<div class="demo-fields">
			<label
				>View distance (chunks)<input type="number" min="2" max="32" bind:value={distance} /></label
			><CarbonButton
				kind="secondary"
				size="sm"
				disabled={!valid || (distance === liveDistance && !batchItems.length)}
				onclick={apply}>Apply now</CarbonButton
			><CarbonButton size="sm" disabled={!valid || distance === liveDistance} onclick={stage}
				>Stage</CarbonButton
			>
		</div>
		{#if !valid}<p class="demo-result" role="alert">Choose a whole number from 2 to 32.</p>{/if}
		<UpdateBatch
			bind:applyWindow
			bind:applyAtLocal
			bind:idleMinutes
			batchOpen={batchItems.length > 0}
			{batchItems}
			{discardStaged}
		/>
		<div class="demo-result" role="status">{message}</div>
		<div class="px-5 py-2"><button class="demo-reset" onclick={reset}>↺ Reset example</button></div>
	</fieldset>
</div>
