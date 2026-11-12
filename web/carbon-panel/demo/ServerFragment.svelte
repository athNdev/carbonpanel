<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import ServerLifecycleActions from '$lib/components/fragments/ServerLifecycleActions.svelte';
	import CarbonTag from '$lib/components/carbon/CarbonTag.svelte';
	import { ServerStatus } from '$lib/proto/carbonpanel/v1/common_pb';
	let ready = $state(false);
	let status = $state(ServerStatus.RUNNING);
	let busy = $state(false);
	let message = $state('Try restarting this sample server.');
	let timer: ReturnType<typeof setTimeout> | undefined;
	let fragmentRoot: HTMLDivElement;
	function publishWorldState() {
		fragmentRoot?.dispatchEvent(
			new CustomEvent('carbon-demo-server-state', {
				bubbles: true,
				composed: true,
				detail: { running: status === ServerStatus.RUNNING }
			})
		);
	}
	onMount(() => {
		ready = true;
		publishWorldState();
	});
	onDestroy(() => clearTimeout(timer));
	function reset() {
		clearTimeout(timer);
		status = ServerStatus.RUNNING;
		busy = false;
		message = 'Sample restored. Try the server controls.';
		publishWorldState();
	}
	function act(action: 'start' | 'stop' | 'restart') {
		if (busy) return;
		busy = true;
		status = action === 'stop' ? ServerStatus.STOPPING : ServerStatus.STARTING;
		message =
			action === 'restart'
				? 'Simulating a restart. Your real servers are unaffected.'
				: `Simulating ${action === 'stop' ? 'a stop' : 'a start'}…`;
		timer = setTimeout(() => {
			status = action === 'stop' ? ServerStatus.STOPPED : ServerStatus.RUNNING;
			busy = false;
			publishWorldState();
			message =
				action === 'stop'
					? 'Sample server stopped. Start it again when you are ready.'
					: 'Sample server is running. Ready for the next adventure.';
		}, 1000);
	}
</script>

<div class="fragment-root" data-carbon-theme="dark" bind:this={fragmentRoot}>
	<fieldset disabled={!ready}>
		<div class="demo-heading">
			<div>
				<h3>The Overworld</h3>
				<p>MINECRAFT SERVER</p>
			</div>
			<CarbonTag
				type={status === ServerStatus.RUNNING
					? 'green'
					: status === ServerStatus.STOPPED
						? 'gray'
						: 'blue'}
				>{status === ServerStatus.RUNNING
					? 'Running'
					: status === ServerStatus.STOPPED
						? 'Stopped'
						: status === ServerStatus.STOPPING
							? 'Stopping'
							: 'Starting'}</CarbonTag
			>
		</div>
		<div class="demo-body">
			<dl class="demo-metadata">
				<div>
					<dt>SOFTWARE</dt>
					<dd>Paper · 1.21.1</dd>
				</div>
				<div>
					<dt>PLAYER ADDRESS</dt>
					<dd><code>overworld.example.com</code></dd>
				</div>
			</dl>
			<ServerLifecycleActions {status} actionLoading={busy} onaction={act} />
		</div>
		<div class="demo-result" role="status">{message}</div>
		<div class="px-5 py-2"><button class="demo-reset" onclick={reset}>↺ Reset example</button></div>
	</fieldset>
</div>
