<!-- SPDX-License-Identifier: MPL-2.0 -->
<script>
	import { tick } from 'svelte';
	import ServerConsole from '$lib/components/server-console.svelte';
	import { ServerStatus } from '$lib/proto/carbonpanel/v1/common_pb';

	let serverId = $state('server-A');
	let container = $state('container-A');
	let server = $derived({
		id: serverId,
		name: serverId,
		containerId: container,
		status: ServerStatus.RUNNING
	});

	// The browser driver owns navigation, recreation and settling; the real
	// ServerConsole component and its ownership guards are under test.
	globalThis.__oss245Ui = {
		setServer(next) {
			serverId = next;
		},
		recreate() {
			container = `container-${serverId}-recreated`;
		},
		flush: async () => {
			await tick();
			await tick();
		}
	};
</script>

<main data-testid="oss245-app">
	<ServerConsole {server} active={true} />
</main>
