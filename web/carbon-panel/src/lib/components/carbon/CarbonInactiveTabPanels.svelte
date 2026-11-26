<!-- SPDX-License-Identifier: MPL-2.0 -->

<script lang="ts">
	let {
		tabs,
		selectedTab,
		idPrefix
	}: {
		tabs: readonly { id: string }[];
		selectedTab: string;
		idPrefix: string;
	} = $props();
</script>

<!-- The route owns the selected panel and mounts its content on demand. -->
{#each tabs as tab (tab.id)}
	{#if tab.id !== selectedTab}
		<div
			id="{idPrefix}-panel-{tab.id}"
			role="tabpanel"
			aria-labelledby="{idPrefix}-tab-{tab.id}"
			hidden
		></div>
	{/if}
{/each}
