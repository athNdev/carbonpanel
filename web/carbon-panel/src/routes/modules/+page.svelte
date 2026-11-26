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
	import { CarbonTabs, CarbonPageHeader } from '$lib/components/carbon';
	import CarbonInactiveTabPanels from '$lib/components/carbon/CarbonInactiveTabPanels.svelte';
	import TemplateManagement from './TemplateManagement.svelte';
	import ActiveModules from './ActiveModules.svelte';

	let activeTab = $state('templates');

	const moduleTabs = [
		{ id: 'templates', label: 'Templates' },
		{ id: 'active', label: 'Active Instances' }
	];
</script>

<svelte:head>
	<title>Modules & Sidecars - Carbon Panel</title>
</svelte:head>

<div class="h-full flex-1 space-y-6 rounded-none font-sans text-foreground">
	<!-- Top Bar -->
	<CarbonPageHeader
		title="Modules & Sidecars"
		description="Manage global module templates and inspect active sidecar container instances"
	/>

	<!-- Carbon Navigation Tabs -->
	<CarbonTabs tabs={moduleTabs} bind:selectedTab={activeTab} idPrefix="modules" />
	<CarbonInactiveTabPanels tabs={moduleTabs} selectedTab={activeTab} idPrefix="modules" />

	{#key activeTab}
		<div
			id="modules-panel-{activeTab}"
			role="tabpanel"
			aria-labelledby="modules-tab-{activeTab}"
			class="focus-ring motion-fade-in"
			tabindex="0"
		>
			{#if activeTab === 'templates'}
				<TemplateManagement />
			{:else if activeTab === 'active'}
				<ActiveModules />
			{/if}
		</div>
	{/key}
</div>
