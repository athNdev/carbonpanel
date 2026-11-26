<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import NativeMapViewport from '../../src/lib/components/worldview/NativeMapViewport.svelte';
	import { nativeMapMarkers } from '../../src/lib/features/worldview/native-map-markers';
	import { MapOverlaySchema } from '../../src/lib/proto/carbonpanel/v1/worldview_pb';

	let session = $state('fixture-one');
	const markers = nativeMapMarkers(
		[
			{ id: 'marker', kind: 'marker', label: 'River camp', x: 36.5, z: -14.25 },
			{ id: 'poi', kind: 'poi', label: 'Old aqueduct', x: -24, z: -69 },
			{ id: 'waypoint', kind: 'waypoint', label: 'North trail', x: -300, z: 270 },
			{ id: 'private', kind: 'marker', label: 'Private location', x: 5, z: 10, sensitive: true },
			{ id: 'player', kind: 'player', label: 'Player location', x: 5, z: 10 },
			{ id: 'region', kind: 'region', label: 'Unsupported region', x: 5, z: 10 },
			{ id: 'nether', kind: 'poi', label: 'Nether ruin', x: 5, z: 10, dimension: 'nether' }
		].map((overlay) => create(MapOverlaySchema, { dimension: 'overworld', ...overlay }))
	);
</script>

<main class="mx-auto max-w-6xl bg-background p-3 sm:p-5">
	<div hidden>
		<button id="switch-native-session" type="button" onclick={() => (session = 'fixture-two')}>
			Switch fixture session
		</button>
	</div>
	<NativeMapViewport
		title="Survival map"
		proxyPath={'/api/v1/worldview/map/' + session + '/'}
		{markers}
	/>
</main>
