<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { ServerStatus, type Server } from '../../src/lib/proto/carbonpanel/v1/common_pb';
	import ServerArchiveExport from '../../src/lib/features/servers/ServerArchiveExport.svelte';

	let server = $state<Server>({
		id: 'server-a',
		name: 'Alpha',
		status: ServerStatus.STOPPED
	} as Server);

	export function switchServer() {
		server = { id: 'server-b', name: 'Beta', status: ServerStatus.STOPPED } as Server;
	}
</script>

<ServerArchiveExport {server} />
