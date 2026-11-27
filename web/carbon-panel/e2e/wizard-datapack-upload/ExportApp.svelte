<!-- SPDX-License-Identifier: MPL-2.0 -->
<script>
	import { Toaster } from 'svelte-sonner';
	import { ServerStatus } from '$lib/proto/carbonpanel/v1/common_pb';
	import ServerArchiveExport from '../../src/lib/features/servers/ServerArchiveExport.svelte';

	// `?running=1` renders the same export control against a live server so the
	// fixture can prove the disabled/refusal state without a live daemon.
	const running = new URLSearchParams(window.location.search).get('running') === '1';
	let server = $state({
		id: 'fixture-server',
		name: 'Fixture Archive Server',
		status: running ? ServerStatus.RUNNING : ServerStatus.STOPPED
	});

	// The fence must also release an in-flight preparation when the detail route
	// polls a different server identity or status.
	globalThis.__exportSwitchServer = (id, status = ServerStatus.STOPPED) => {
		server = { id, name: `Fixture ${id}`, status };
	};
</script>

<Toaster position="top-right" />
<div class="min-h-screen bg-background p-4 text-foreground sm:p-8">
	<div class="mx-auto max-w-4xl">
		<h1 class="mb-4 text-lg font-semibold">Fixture server detail</h1>
		<ServerArchiveExport {server} />
	</div>
</div>
