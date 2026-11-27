<!-- SPDX-License-Identifier: MPL-2.0 -->
<!-- Test-only stand-in for the paneforge wrappers: renders pane children
     without requiring the package's `svelte` export condition. -->
<script lang="ts">
	import type { Snippet } from 'svelte';
	let { children }: { children?: Snippet } = $props();
</script>

{@render children?.()}
