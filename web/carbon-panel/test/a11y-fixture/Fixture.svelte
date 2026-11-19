<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import CarbonButton from '../../src/lib/components/carbon/CarbonButton.svelte';
	import CarbonErrorState from '../../src/lib/components/carbon/CarbonErrorState.svelte';
	import CarbonModal from '../../src/lib/components/carbon/CarbonModal.svelte';
	import CarbonTabs from '../../src/lib/components/carbon/CarbonTabs.svelte';
	import UpdateBatch from '../../src/lib/components/fragments/UpdateBatch.svelte';

	let selectedTab = $state('overview');
	let dialogOpen = $state(false);
	let showError = $state(false);
</script>

<!-- eslint-disable svelte/no-navigation-without-resolve -- fixture uses fixed, resolved-in-test paths -->
<header class="flex items-center gap-3 border-b border-border p-4 font-sans text-sm">
	<a href="/servers" class="focus-ring" id="crumb-servers">Servers</a>
	<button type="button" class="focus-ring" id="header-action">New server</button>
</header>

<main class="font-sans">
	<nav aria-label="Breadcrumbs" class="flex gap-1 p-4 text-xs">
		<a href="/servers" class="focus-ring" id="nav-servers">Servers</a>
		<span>Example</span>
	</nav>

	<CarbonTabs
		idPrefix="a11y"
		tabs={[
			{ id: 'overview', label: 'Overview' },
			{ id: 'files', label: 'Files', badge: 2 },
			{ id: 'logs', label: 'Logs' }
		]}
		bind:selectedTab
	/>

	<div class="flex flex-wrap gap-2 p-4">
		<CarbonButton id="open-dialog" onclick={() => (dialogOpen = true)}>Open dialog</CarbonButton>
		<CarbonButton id="toggle-state" kind="secondary" onclick={() => (showError = !showError)}>
			{showError ? 'Show empty' : 'Show error'}
		</CarbonButton>
	</div>

	<section class="p-4" aria-label="Content state">
		{#if showError}
			<CarbonErrorState
				title="Failed to load server"
				message="The request could not be completed."
				retryLabel="Retry"
			/>
		{:else}
			<UpdateBatch
				batchOpen={true}
				batchItems={[{ id: 'mod-1', kind: 'mod', summary: 'Example staged change' }]}
				discardStaged={() => {}}
			/>
		{/if}
	</section>
</main>

<CarbonModal
	open={dialogOpen}
	title="Stop server?"
	description="Running services will be interrupted."
	primaryButtonText="Stop server"
	secondaryButtonText="Cancel"
	onclose={() => (dialogOpen = false)}
	onprimary={() => (dialogOpen = false)}
>
	<p class="font-sans text-sm">Confirm the action with the keyboard.</p>
</CarbonModal>
<!-- eslint-enable svelte/no-navigation-without-resolve -->
