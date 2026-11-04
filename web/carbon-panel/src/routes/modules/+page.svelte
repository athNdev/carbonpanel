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

<div class="h-full flex-1 space-y-6 rounded-none font-sans text-[#f4f4f4]">
	<!-- Top Bar -->
	<CarbonPageHeader
		title="Modules & Sidecars"
		description="Manage global module templates and inspect active sidecar container instances"
	/>

	<!-- Carbon Navigation Tabs -->
	<CarbonTabs tabs={moduleTabs} bind:selectedTab={activeTab} />

	{#key activeTab}
		<div class="motion-fade-in">
			{#if activeTab === 'templates'}
				<TemplateManagement />
			{:else if activeTab === 'active'}
				<ActiveModules />
			{/if}
		</div>
	{/key}
</div>
