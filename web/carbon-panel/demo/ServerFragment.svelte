<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { RotateCcw } from '@lucide/svelte';
	import ServerLifecycleActions from '$lib/components/fragments/ServerLifecycleActions.svelte';
	import CarbonTag from '$lib/components/carbon/CarbonTag.svelte';
	import { ServerStatus } from '$lib/proto/carbonpanel/v1/common_pb';
	let ready = $state(false);
	let status = $state(ServerStatus.RUNNING);
	let busy = $state(false);
	let message = $state('Server running');
	const initialActivity = [
		'World loaded: The Overworld',
		'Alex, River and Sam joined',
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
		message = 'Server reset';
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
		activity = [action === 'start' ? 'Starting container…' : 'Saving world and player data…'];
		message =
			action === 'stop'
				? 'Stopping server'
				: action === 'restart'
					? 'Restarting server'
					: 'Starting server';
		timer = setTimeout(() => {
			status = action === 'stop' ? ServerStatus.STOPPED : ServerStatus.RUNNING;
			busy = false;
			publishWorldState();
			activity =
				action === 'stop'
					? ['World and player data saved', 'Container stopped · memory released']
					: [
							'World loaded: The Overworld',
							'Server ready · accepting connections',
							'Alex, River and Sam rejoined'
						];
			message = action === 'stop' ? 'Server stopped' : 'Server running';
		}, 1000);
	}
</script>

<div class="fragment-root server-demo" data-carbon-theme="dark" bind:this={fragmentRoot}>
	<fieldset disabled={!ready}>
		<header class="demo-heading">
			<div class="demo-identity">
				<span class="demo-eyebrow">MINECRAFT SERVER</span>
				<h3>The Overworld</h3>
				<p>Paper · Minecraft 1.21.1</p>
			</div>
			<div class="demo-heading-actions">
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
				><button class="demo-reset" onclick={reset} aria-label="Reset example" title="Reset example"
					><RotateCcw size={15} aria-hidden="true" /></button
				>
			</div>
		</header>
		<dl class="demo-metrics server-resources" aria-label="Sample server resources">
			<div>
				<dt>Players</dt>
				<dd>{status === ServerStatus.RUNNING ? '3' : '0'}<small>/ 20</small></dd>
			</div>
			<div>
				<dt>Memory</dt>
				<dd>{status === ServerStatus.RUNNING ? '1.8' : busy ? '—' : '0'}<small>/ 4 GiB</small></dd>
			</div>
		</dl>
		<dl class="server-address">
			<div>
				<dt>Player address</dt>
				<dd><code>overworld.example.com</code></dd>
			</div>
		</dl>
		<div class="server-controls">
			<ServerLifecycleActions {status} actionLoading={busy} onaction={act} />
		</div>
		<details class="server-activity" bind:open={showActivity}>
			<summary
				>Activity<span
					>{busy ? 'In progress' : status === ServerStatus.RUNNING ? '3 online' : 'Stopped'}</span
				></summary
			>
			<ol>
				{#each activity as line (line)}<li>{line}</li>{/each}
			</ol>
		</details>
		<div class="sr-only" role="status">{message}</div>
	</fieldset>
</div>
