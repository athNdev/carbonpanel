<!--
SPDX-License-Identifier: MPL-2.0
-->

<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { resolve } from '$app/paths';
	import { CarbonButton } from '$lib/components/carbon';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { formatRpcError } from '$lib/api/rpc-error';
	import {
		adaptBlueMapRpc,
		blueMapInstallEligibility,
		installBlueMap,
		retainedBlueMapSetup,
		type InstallOutcome,
		type BlueMapInstallRpc,
		type InstallEligibility
	} from '$lib/features/worldview/bluemap-install';
	import { ListNodesRequestSchema } from '$lib/proto/carbonpanel/v1/node_pb';
	import type { WorldViewTarget } from '$lib/proto/carbonpanel/v1/worldview_pb';

	// rpcClient groups RPCs by service; adapt to the flat install seam so a
	// bare type cast cannot leave listModules/createModule undefined at runtime.
	const installRpc: BlueMapInstallRpc = adaptBlueMapRpc(rpcClient, silentCallOptions);

	interface Props {
		serverId: string;
		target: WorldViewTarget | undefined;
		can: (resource: string, action: string) => boolean;
		oncomplete: () => void;
	}

	let { serverId, target, can, oncomplete }: Props = $props();

	let nodes = $state<Awaited<ReturnType<BlueMapInstallRpc['listNodes']>>['nodes']>([]);
	let canInstall = $derived(can('modules', 'create') && can('modules', 'start'));
	let nodesLoading = $state(false);
	let nodesError = $state(false);
	let showingConsent = $state(false);
	let accepted = $state(false);
	let reuseConfiguration = $state(false);
	let busy = $state(false);
	let message = $state('');
	let outcome = $state<'idle' | InstallOutcome | 'error'>('idle');
	let setupProblem = $state<ReturnType<typeof retainedBlueMapSetup>>(null);
	const setupMessages: Record<InstallOutcome, string> = {
		installed: 'BlueMap is starting. The view switches after the first tiles are ready.',
		reused: 'BlueMap is starting with the existing configuration.',
		adopted: 'BlueMap already exists. Check its status in the Modules tab.'
	};
	let serverHref = $derived(resolve('/servers/[id]', { id: serverId }));

	function beginSetup(reuse: boolean) {
		reuseConfiguration = reuse;
		accepted = false;
		setupProblem = null;
		showingConsent = true;
	}

	async function loadNodes() {
		nodesLoading = true;
		nodesError = false;
		try {
			nodes = (await installRpc.listNodes(create(ListNodesRequestSchema))).nodes;
		} catch {
			nodes = [];
			nodesError = true;
		} finally {
			nodesLoading = false;
		}
	}

	$effect(() => {
		if (canInstall) void loadNodes();
	});

	let eligibility = $derived<InstallEligibility>(blueMapInstallEligibility(can, nodes, target));

	async function confirm(reuse = reuseConfiguration) {
		if (!accepted || busy || !eligibility.eligible) return;
		busy = true;
		message = '';
		outcome = 'idle';
		try {
			const result = await installBlueMap(installRpc, serverId, {
				accepted: true,
				reuseConfiguration: reuse
			});
			outcome = result.outcome;
			message = setupMessages[result.outcome];
			setupProblem = null;
			showingConsent = false;
			accepted = false;
			oncomplete();
		} catch (error) {
			setupProblem = retainedBlueMapSetup(error);
			if (setupProblem) {
				reuseConfiguration = true;
				showingConsent = false;
			} else {
				outcome = 'error';
				message = formatRpcError(error, 'BlueMap could not be installed.');
			}
		} finally {
			busy = false;
		}
	}
</script>

<div class="space-y-3">
	{#if canInstall && nodesLoading}
		<p role="status" class="text-sm text-muted-foreground">Checking renderer setup options…</p>
	{:else if canInstall && nodesError}
		<p role="alert" class="text-sm text-destructive-text">
			Could not check whether BlueMap can be installed on this node.
		</p>
		<CarbonButton kind="secondary" size="sm" onclick={loadNodes}>Retry setup check</CarbonButton>
	{:else if !eligibility.eligible}
		<p class="text-sm text-muted-foreground">{eligibility.reason}</p>
	{:else if setupProblem === 'config-missing'}
		<p role="status" class="text-sm text-muted-foreground">
			No saved BlueMap configuration was found.
		</p>
		<CarbonButton kind="primary" size="sm" onclick={() => beginSetup(false)}
			>Install BlueMap</CarbonButton
		>
	{:else if setupProblem}
		<div class="space-y-2 border-l-2 border-accent-text pl-3">
			<p class="text-sm font-semibold">BlueMap configuration found</p>
			<p role="status" class="text-sm text-muted-foreground">
				{#if setupProblem === 'download-consent'}
					Review the download terms in <code>modules/bluemap/config/core.conf</code>. If you agree,
					set <code>accept-download: true</code> and save the file.
				{:else if setupProblem === 'world-config'}
					In <code>modules/bluemap/config/maps</code>, point the Overworld map at
					<code>/app/world</code>.
				{:else}
					Start BlueMap with the settings already saved for this server.
				{/if}
			</p>
			<div class="flex flex-wrap gap-2">
				<CarbonButton
					kind="primary"
					size="sm"
					disabled={busy}
					loading={busy}
					onclick={() => void confirm(true)}
				>
					{setupProblem === 'retained-config' ? 'Use existing configuration' : 'Check and start'}
				</CarbonButton>
				{#if setupProblem !== 'retained-config'}
					<CarbonButton kind="secondary" size="sm" href={serverHref + '?tab=files'}>
						Open server files
					</CarbonButton>
				{/if}
			</div>
		</div>
	{:else if !showingConsent && (outcome === 'idle' || outcome === 'error')}
		<div class="flex flex-wrap gap-2">
			<CarbonButton kind="primary" size="sm" onclick={() => beginSetup(false)}>
				Install BlueMap
			</CarbonButton>
			<CarbonButton kind="tertiary" size="sm" onclick={() => beginSetup(true)}>
				Use existing configuration
			</CarbonButton>
		</div>
	{:else if showingConsent}
		<div class="space-y-3">
			<h3 class="text-sm font-semibold">
				{reuseConfiguration ? 'Reuse BlueMap configuration' : 'Before installing BlueMap'}
			</h3>
			<ul class="list-disc space-y-1 pl-5 text-xs text-muted-foreground">
				<li>Java Edition worlds only; the renderer version is pinned to v5.29.</li>
				<li>
					BlueMap is MIT licensed and free. Rendering needs a one-time download of Mojang client
					resources.
				</li>
				<li>
					Accepting confirms you accept the Minecraft EULA, own a Java Edition licence, and agree to
					that download. The resources are not redistributed by Carbon Panel.
				</li>
				<li>The world mount is read-only and no renderer port is published publicly.</li>
			</ul>
			<label class="flex items-start gap-2 text-sm">
				<input type="checkbox" bind:checked={accepted} class="mt-1" />
				<span>
					I explicitly accept the Minecraft EULA, confirm I own a Java Edition licence, and allow
					the Mojang resource download.
				</span>
			</label>
			<div class="flex flex-wrap gap-2">
				<CarbonButton
					kind="primary"
					size="sm"
					disabled={!accepted || busy}
					loading={busy}
					onclick={() => void confirm()}
				>
					{reuseConfiguration ? 'Accept and start' : 'Accept and install'}
				</CarbonButton>
				<CarbonButton
					kind="tertiary"
					size="sm"
					disabled={busy}
					onclick={() => (showingConsent = false)}
				>
					Cancel
				</CarbonButton>
			</div>
		</div>
	{/if}
	{#if message}
		<p
			class="text-xs {outcome === 'error' ? 'text-destructive-text' : 'text-muted-foreground'}"
			role={outcome === 'error' ? 'alert' : 'status'}
		>
			{message}
		</p>
	{/if}
	{#if outcome === 'adopted'}
		<CarbonButton kind="secondary" size="sm" href={serverHref + '?tab=modules'}>
			Open Modules
		</CarbonButton>
	{/if}
</div>
