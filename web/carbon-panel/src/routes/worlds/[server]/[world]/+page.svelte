<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { page } from '$app/state';
	import WorldViewWorkspace from '$lib/components/worldview/WorldViewWorkspace.svelte';
</script>

{#key JSON.stringify([page.params.server, page.params.world])}
	<WorldViewWorkspace serverId={page.params.server ?? ''} worldKey={page.params.world ?? ''} />
{/key}
