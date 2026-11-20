<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import NativeMapViewport from '../../src/lib/components/worldview/NativeMapViewport.svelte';

	let session = $state('fixture-one');
</script>

<main class="mx-auto max-w-6xl bg-background p-3 sm:p-5">
	<div hidden>
		<button id="switch-native-session" type="button" onclick={() => (session = 'fixture-two')}>
			Switch fixture session
		</button>
	</div>
	<NativeMapViewport title="Survival map" proxyPath={'/api/v1/worldview/map/' + session + '/'} />
</main>
