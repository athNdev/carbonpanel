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
	const initialActivity = [
		'World loaded: The Overworld',
		'Alex, River and Sam joined the sample server',
		'Server ready · Paper 1.21.1'
	];
	let activity = $state([...initialActivity]);
	let showActivity = $state(false);
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
		activity = [...initialActivity];
		showActivity = false;
		publishWorldState();
	}
	function act(action: 'start' | 'stop' | 'restart') {
		if (busy) return;
		busy = true;
		status = action === 'stop' ? ServerStatus.STOPPING : ServerStatus.STARTING;
		publishWorldState();
		showActivity = true;
		activity = [
			action === 'start' ? 'Starting the sample container…' : 'Saving world and player data…'
		];
		message =
			action === 'restart'
				? 'Simulating a restart. Your real servers are unaffected.'
				: `Simulating ${action === 'stop' ? 'a stop' : 'a start'}…`;
		timer = setTimeout(() => {
			status = action === 'stop' ? ServerStatus.STOPPED : ServerStatus.RUNNING;
			busy = false;
			publishWorldState();
			activity =
				action === 'stop'
					? ['World and player data saved', 'Sample container stopped · memory released']
					: [
							'World loaded: The Overworld',
							'Server ready · accepting connections',
							'Alex, River and Sam rejoined the sample server'
						];
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
		<dl class="server-resources" aria-label="Sample server resources">
			<div>
				<dt>PLAYERS</dt>
				<dd>{status === ServerStatus.RUNNING ? '3 / 20' : '0 / 20'}</dd>
			</div>
			<div>
				<dt>MEMORY</dt>
				<dd>
					{status === ServerStatus.RUNNING
						? '1.8 / 4 GiB'
						: busy
							? 'Starting / stopping'
							: '0 / 4 GiB'}
				</dd>
			</div>
		</dl>
		<details class="server-activity" bind:open={showActivity}>
			<summary
				>Sample activity <span
					>{busy
						? 'Working'
						: status === ServerStatus.RUNNING
							? '3 players online'
							: 'World safely stopped'}</span
				></summary
			>
			<ol>
				{#each activity as line (line)}<li>{line}</li>{/each}
			</ol>
		</details>
		<div class="demo-result" role="status">{message}</div>
		<div class="demo-footer">
			<span>Sample server · nothing connected</span><button class="demo-reset" onclick={reset}
				>↺ Reset example</button
			>
		</div>
	</fieldset>
</div>
