<!--
SPDX-License-Identifier: MPL-2.0
-->

<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { CarbonButton } from '$lib/components/carbon';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { formatRpcError } from '$lib/api/rpc-error';
	import {
		adaptBlueMapRpc,
		blueMapInstallEligibility,
		installBlueMap,
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
	let busy = $state(false);
	let message = $state('');
	let outcome = $state<'idle' | 'installed' | 'adopted' | 'error'>('idle');

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

	async function confirm() {
		if (!accepted || busy || !eligibility.eligible) return;
		busy = true;
		message = '';
		outcome = 'idle';
		try {
			const result = await installBlueMap(installRpc, serverId, {
				accepted: true
			});
			outcome = result.outcome;
			message =
				result.outcome === 'adopted'
					? 'An existing BlueMap module was adopted. Its config was not changed.'
					: 'BlueMap is being created and started through the module service.';
			showingConsent = false;
			accepted = false;
			oncomplete();
		} catch (error) {
			outcome = 'error';
			message = formatRpcError(error, 'BlueMap could not be installed.');
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
	{:else if !showingConsent && outcome === 'idle'}
		<CarbonButton kind="primary" size="sm" onclick={() => (showingConsent = true)}>
			Install BlueMap
		</CarbonButton>
		<p class="text-xs text-muted-foreground">
			Requires an explicit one-time download/licence acknowledgement for a new install. Existing
			BlueMap modules are adopted without changing their configuration.
		</p>
	{:else if showingConsent}
		<div class="space-y-3">
			<h3 class="text-sm font-semibold">Before installing BlueMap</h3>
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
				<span
					>I explicitly accept the Minecraft EULA, confirm a Java Edition licence, and allow the
					Mojang resource download for this new BlueMap install.</span
				>
			</label>
			<div class="flex flex-wrap gap-2">
				<CarbonButton
					kind="primary"
					size="sm"
					disabled={!accepted || busy}
					loading={busy}
					onclick={() => void confirm()}
				>
					Accept and install
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
</div>
