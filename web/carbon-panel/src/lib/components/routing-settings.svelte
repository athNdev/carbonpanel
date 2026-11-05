<!--
SPDX-License-Identifier: MIT AND MPL-2.0
This file includes DiscoPanel material under the MIT License,
Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
and still applies to that inherited material.
Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
If a copy of the MPL was not distributed with this file, You can obtain
one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { onMount } from 'svelte';
	import { rpcClient } from '$lib/api/rpc-client';
	import type { ProxyListener } from '$lib/proto/carbonpanel/v1/common_pb';
	import type { ProxyListenerWithCount, ProxyRoute } from '$lib/proto/carbonpanel/v1/proxy_pb';
	import {
		CarbonButton,
		CarbonInlineLoading,
		CarbonConfirm,
		CarbonTextInput
	} from '$lib/components/carbon';
	import { toast } from 'svelte-sonner';
	import {
		Save,
		Plus,
		Trash2,
		Loader2,
		AlertCircle,
		Server,
		Activity,
		CheckCircle2,
		XCircle,
		Network,
		Info,
		Edit,
		Star
	} from '@lucide/svelte';

	let loading = $state(true);
	let saving = $state(false);
	let proxyEnabled = $state(false);
	let baseURL = $state('');
	const baseDomainHelp =
		'Optional base domain that will be appended to server hostnames (e.g., "survival" becomes "survival.minecraft.example.com")';
	let listenersWithCount = $state<ProxyListenerWithCount[]>([]);
	let editingListener = $state<ProxyListener | null>(null);
	let newListener = $state<{
		port: number | undefined;
		name: string;
		description: string;
		enabled: boolean;
		isDefault: boolean;
	}>({
		port: 25565,
		name: '',
		description: '',
		enabled: true,
		isDefault: false
	});
	// Kept as text so the port field is not rewritten while the operator is typing.
	let newListenerPort = $state('25565');
	let portError = $state('');
	let activeRoutes = $state<ProxyRoute[]>([]);

	onMount(() => {
		loadAll();
	});

	async function loadAll() {
		loading = true;
		try {
			await Promise.all([loadProxyConfig(), loadListeners(), loadActiveRoutes()]);
		} finally {
			loading = false;
		}
	}

	async function loadProxyConfig() {
		try {
			const status = await rpcClient.proxy.getProxyStatus({});
			proxyEnabled = status.enabled;
			baseURL = status.baseUrl || '';
		} catch (_e) {
			toast.error('Failed to load proxy configuration');
		}
	}

	async function loadListeners() {
		try {
			const response = await rpcClient.proxy.getProxyListeners({});
			listenersWithCount = response.listeners;
			// Set default port for new listener
			if (listenersWithCount.length > 0) {
				const usedPorts = new Set(listenersWithCount.map((lwc) => lwc.listener?.port || 0));
				let nextPort = 25565;
				while (usedPorts.has(nextPort)) {
					nextPort++;
				}
				newListener.port = nextPort;
				newListenerPort = String(nextPort);
			}
		} catch (_e) {
			toast.error('Failed to load proxy listeners');
		}
	}

	async function loadActiveRoutes() {
		try {
			const response = await rpcClient.proxy.getProxyRoutes({});
			activeRoutes = response.routes;
		} catch (error) {
			console.error('Failed to load active routes:', error);
		}
	}

	function validatePort(port: number): boolean {
		portError = '';

		if (!port || port < 1 || port > 65535) {
			portError = 'Port must be between 1 and 65535';
			return false;
		}

		// Check if port is already used by another listener
		const existingListener = listenersWithCount.find(
			(lwc) => lwc.listener?.port === port && lwc.listener?.id !== editingListener?.id
		);
		if (existingListener) {
			portError = `Port ${port} is already used by listener "${existingListener.listener?.name}"`;
			return false;
		}

		return true;
	}

	async function saveProxyConfig() {
		saving = true;
		try {
			await rpcClient.proxy.updateProxyConfig({
				enabled: proxyEnabled,
				baseUrl: baseURL
			});

			toast.success('Proxy configuration saved');
			await loadAll();
		} catch (_e) {
			toast.error('Failed to save proxy configuration');
		} finally {
			saving = false;
		}
	}

	async function createListener() {
		if (!newListener.name) {
			toast.error('Listener name is required');
			return;
		}

		if (!validatePort(newListener.port!)) {
			return;
		}

		try {
			await rpcClient.proxy.createProxyListener({
				port: newListener.port!,
				name: newListener.name,
				description: newListener.description || '',
				enabled: newListener.enabled,
				isDefault: newListener.isDefault
			});

			toast.success(`Listener "${newListener.name}" created`);

			// Reset form
			newListener = {
				port: 25565,
				name: '',
				description: '',
				enabled: true,
				isDefault: false
			};
			newListenerPort = '25565';

			await loadListeners();
		} catch (error: unknown) {
			toast.error(error instanceof Error ? error.message : 'Failed to create listener');
		}
	}

	async function updateListener(listener: ProxyListener) {
		try {
			await rpcClient.proxy.updateProxyListener({
				id: listener.id,
				name: listener.name,
				description: listener.description,
				enabled: listener.enabled,
				isDefault: listener.isDefault
			});

			toast.success(`Listener "${listener.name}" updated`);
			editingListener = null;
			await loadListeners();
		} catch (_e) {
			toast.error('Failed to update listener');
		}
	}

	let pendingDelete = $state<ProxyListenerWithCount | null>(null);
	let confirmOpen = $state(false);

	async function deleteListener(listenerWithCount: ProxyListenerWithCount) {
		const listener = listenerWithCount.listener;
		if (!listener) return;

		// Check server count from the response
		if (listenerWithCount.serverCount > 0) {
			toast.error(
				`Cannot delete: ${listenerWithCount.serverCount} servers are using this listener`
			);
			return;
		}

		pendingDelete = listenerWithCount;
		confirmOpen = true;
	}

	async function confirmDeleteListener() {
		const target = pendingDelete;
		if (!target?.listener) return;
		const listener = target.listener;
		try {
			await rpcClient.proxy.deleteProxyListener({ id: listener.id });
			toast.success(`Listener "${listener.name}" deleted`);
			await loadListeners();
		} catch (error: unknown) {
			toast.error(error instanceof Error ? error.message : 'Failed to delete listener');
		} finally {
			pendingDelete = null;
		}
	}

	async function setDefaultListener(listener: ProxyListener) {
		listener.isDefault = true;
		await updateListener(listener);
	}

	function getListenerStatus(
		listener: ProxyListener | undefined,
		serverCount: number
	): 'active' | 'inactive' | 'disabled' {
		if (!listener || !listener.enabled) return 'disabled';
		if (!proxyEnabled) return 'inactive';
		return serverCount > 0 ? 'active' : 'inactive';
	}

	function getStatusColor(status: string): string {
		switch (status) {
			case 'active':
				return 'text-green-500';
			case 'inactive':
				return 'text-yellow-500';
			case 'disabled':
				return 'text-gray-500';
			default:
				return 'text-gray-500';
		}
	}

	function getStatusIcon(status: string) {
		switch (status) {
			case 'active':
				return CheckCircle2;
			case 'disabled':
				return XCircle;
			default:
				return AlertCircle;
		}
	}
</script>

<div class="space-y-6">
	<!-- Global Proxy Configuration -->
	<div class="space-y-4 rounded-none border border-[#393939] bg-[#262626] p-5 shadow-none">
		<div class="flex items-center justify-between border-b border-[#393939] pb-4">
			<div class="flex items-center gap-3">
				<Network class="h-5 w-5 text-accent-text" />
				<div>
					<h3 class="font-sans text-base font-normal text-[#f4f4f4]">Proxy Configuration</h3>
					<p class="text-xs text-[#a8a8a8]">Global proxy settings and base domain configuration</p>
				</div>
			</div>
			<input
				id="proxy-enabled"
				type="checkbox"
				class="focus-ring h-4 w-4 shrink-0 cursor-pointer accent-[#0f62fe]"
				checked={proxyEnabled}
				onchange={(e) => (proxyEnabled = e.currentTarget.checked)}
				disabled={loading || saving}
				aria-label="Enable proxy system"
			/>
		</div>
		<div class="space-y-4">
			<CarbonTextInput
				id="base-url"
				label="Base Domain"
				bind:value={baseURL}
				placeholder="minecraft.example.com"
				disabled={saving || !proxyEnabled}
				helperText={baseDomainHelp}
				class="[&_input]:bg-[#161616]"
			/>

			<div class="flex justify-end">
				<CarbonButton
					kind="primary"
					size="md"
					class="gap-2 px-6"
					onclick={saveProxyConfig}
					disabled={saving}
				>
					{#if saving}
						<Loader2 class="h-4 w-4 animate-spin" />
						<span>Saving...</span>
					{:else}
						<Save class="h-4 w-4" />
						<span>Save Configuration</span>
					{/if}
				</CarbonButton>
			</div>
		</div>
	</div>

	{#if loading}
		<div
			class="flex items-center justify-center rounded-none border border-[#393939] bg-[#262626] p-12"
		>
			<CarbonInlineLoading description="Loading proxy configuration..." />
		</div>
	{:else if !proxyEnabled}
		<div
			role="status"
			class="flex gap-3 rounded-none border border-[#393939] bg-[#262626] p-4 text-[#c6c6c6]"
		>
			<Info class="mt-0.5 h-4 w-4 shrink-0 text-[#78a9ff]" />
			<p class="text-xs text-[#a8a8a8]">
				Enable the proxy system to allow servers to use custom hostnames instead of direct port
				connections.
			</p>
		</div>
	{:else}
		<!-- Proxy Listeners -->
		<div class="space-y-4 rounded-none border border-[#393939] bg-[#262626] p-5 shadow-none">
			<div class="flex items-center justify-between border-b border-[#393939] pb-4">
				<div>
					<h3 class="font-sans text-base font-normal text-[#f4f4f4]">Proxy Listeners</h3>
					<p class="text-xs text-[#a8a8a8]">Configure individual proxy listening ports</p>
				</div>
				<span
					class="inline-flex items-center gap-1 rounded-none border border-[#525252] bg-[#161616] px-2 py-0.5 font-mono text-xs text-[#c6c6c6]"
				>
					<Server class="h-3 w-3 text-[#78a9ff]" />
					{listenersWithCount.length}
					{listenersWithCount.length === 1 ? 'Listener' : 'Listeners'}
				</span>
			</div>
			<div class="space-y-4">
				<!-- Existing Listeners -->
				{#if listenersWithCount.length > 0}
					<div class="space-y-3">
						{#each listenersWithCount as lwc (lwc.listener?.id)}
							{@const listener = lwc.listener}
							{@const status = getListenerStatus(listener, lwc.serverCount)}
							{@const StatusIcon = getStatusIcon(status)}
							{#if listener}
								<div class="rounded-none border border-[#393939] bg-[#1e1e1e] p-4">
									{#if editingListener?.id === listener.id}
										<!-- Edit Mode -->
										<div class="space-y-3">
											<div class="grid grid-cols-1 gap-3 md:grid-cols-2">
												<CarbonTextInput
													label="Name"
													bind:value={editingListener.name}
													placeholder="Listener name"
													class="[&_input]:bg-[#161616]"
												/>
												<CarbonTextInput
													label="Port"
													type="number"
													value={String(listener.port)}
													disabled
													class="[&_input]:bg-[#161616] [&_input]:text-[#8d8d8d]"
												/>
											</div>
											<CarbonTextInput
												label="Description"
												bind:value={editingListener.description}
												placeholder="Optional description"
												class="[&_input]:bg-[#161616]"
											/>
											<div class="flex items-center justify-between pt-2">
												<div class="flex items-center gap-4">
													<label class="flex cursor-pointer items-center gap-2">
														<input
															type="checkbox"
															class="focus-ring h-4 w-4 cursor-pointer accent-[#0f62fe]"
															checked={editingListener?.enabled ?? false}
															onchange={(e) => {
																if (editingListener)
																	editingListener.enabled = e.currentTarget.checked;
															}}
														/>
														<span class="text-xs text-[#c6c6c6]">Enabled</span>
													</label>
													<label class="flex cursor-pointer items-center gap-2">
														<input
															type="checkbox"
															class="focus-ring h-4 w-4 cursor-pointer accent-[#0f62fe]"
															checked={editingListener?.isDefault ?? false}
															onchange={(e) => {
																if (editingListener)
																	editingListener.isDefault = e.currentTarget.checked;
															}}
														/>
														<span class="text-xs text-[#c6c6c6]">Default</span>
													</label>
												</div>
												<div class="flex gap-2">
													<CarbonButton
														kind="secondary"
														size="sm"
														onclick={() => (editingListener = null)}
													>
														Cancel
													</CarbonButton>
													<CarbonButton
														kind="primary"
														size="sm"
														onclick={() => {
															if (editingListener) updateListener(editingListener);
														}}
													>
														Save
													</CarbonButton>
												</div>
											</div>
										</div>
									{:else}
										<!-- View Mode -->
										<div class="flex items-start justify-between">
											<div class="space-y-2">
												<div class="flex items-center gap-3">
													<StatusIcon class="h-4 w-4 {getStatusColor(status)}" />
													<span class="text-sm font-normal text-[#f4f4f4]">{listener.name}</span>
													<span
														class="rounded-none border border-[#525252] bg-[#161616] px-1.5 py-0.5 font-mono text-xs text-[#c6c6c6]"
														>:{listener.port}</span
													>
													{#if listener.isDefault}
														<span
															class="inline-flex items-center gap-1 rounded-none border border-[#0f62fe] bg-[#0043ce]/20 px-1.5 py-0.5 font-mono text-xs text-[#78a9ff]"
														>
															<Star class="h-3 w-3" />
															Default
														</span>
													{/if}
													{#if !listener.enabled}
														<span
															class="rounded-none border border-[#525252] bg-transparent px-1.5 py-0.5 font-mono text-xs text-[#8d8d8d]"
															>Disabled</span
														>
													{/if}
												</div>

												{#if listener.description}
													<p class="text-xs text-[#a8a8a8]">{listener.description}</p>
												{/if}

												{#if lwc.serverCount > 0}
													<p class="text-xs text-[#8d8d8d]">
														{lwc.serverCount}
														{lwc.serverCount === 1 ? 'server' : 'servers'} using this listener
													</p>
												{:else}
													<p class="text-xs text-[#8d8d8d]">No servers using this listener</p>
												{/if}
											</div>

											<div class="flex gap-1">
												{#if !listener.isDefault}
													<button
														type="button"
														class="inline-flex h-8 w-8 cursor-pointer items-center justify-center rounded-none text-[#8d8d8d] transition-colors hover:bg-[#353535] hover:text-[#f4f4f4]"
														onclick={() => setDefaultListener(listener)}
														title="Set as default"
														aria-label="Set as default"
													>
														<Star class="h-4 w-4" />
													</button>
												{/if}
												<button
													type="button"
													class="inline-flex h-8 w-8 cursor-pointer items-center justify-center rounded-none text-[#8d8d8d] transition-colors hover:bg-[#353535] hover:text-[#f4f4f4]"
													onclick={() => (editingListener = { ...listener })}
													aria-label="Edit listener"
												>
													<Edit class="h-4 w-4" />
												</button>
												{#if listenersWithCount.length > 1 && lwc.serverCount === 0}
													<button
														type="button"
														class="inline-flex h-8 w-8 cursor-pointer items-center justify-center rounded-none text-[#8d8d8d] transition-colors hover:bg-[#da1e28]/20 hover:text-[#ff8389]"
														onclick={() => deleteListener(lwc)}
														aria-label="Delete listener"
													>
														<Trash2 class="h-4 w-4" />
													</button>
												{/if}
											</div>
										</div>
									{/if}
								</div>
							{/if}
						{/each}
					</div>
				{/if}

				<!-- Add New Listener -->
				<div class="border-t border-[#393939] pt-4">
					<h4 class="mb-3 text-sm font-normal text-[#f4f4f4]">Add New Listener</h4>
					<div class="space-y-3">
						<div class="grid grid-cols-1 gap-3 md:grid-cols-2">
							<CarbonTextInput
								label="Name"
								bind:value={newListener.name}
								placeholder="e.g., Secondary, Development"
								class="[&_input]:bg-[#161616]"
							/>
							<CarbonTextInput
								label="Port"
								type="number"
								min={1}
								max={65535}
								bind:value={newListenerPort}
								oninput={(event) => {
									const raw = event.currentTarget.value.trim();
									if (raw === '' || !/^\d+$/.test(raw)) {
										newListener.port = undefined;
										portError = 'Port must be between 1 and 65535';
										return;
									}
									const port = Number(raw);
									newListener.port = port;
									validatePort(port);
								}}
								error={portError}
								class="[&_input]:bg-[#161616]"
							/>
						</div>
						<CarbonTextInput
							label="Description (Optional)"
							bind:value={newListener.description}
							placeholder="Optional description for this listener"
							class="[&_input]:bg-[#161616]"
						/>
						<div class="flex items-center justify-between pt-2">
							<div class="flex items-center gap-4">
								<label class="flex cursor-pointer items-center gap-2">
									<input
										type="checkbox"
										class="focus-ring h-4 w-4 cursor-pointer accent-[#0f62fe]"
										checked={newListener.enabled}
										onchange={(e) => (newListener.enabled = e.currentTarget.checked)}
									/>
									<span class="text-xs text-[#c6c6c6]">Enabled</span>
								</label>
								{#if listenersWithCount.length === 0}
									<label class="flex cursor-pointer items-center gap-2">
										<input
											type="checkbox"
											class="focus-ring h-4 w-4 cursor-pointer accent-[#0f62fe]"
											checked={newListener.isDefault}
											onchange={(e) => (newListener.isDefault = e.currentTarget.checked)}
										/>
										<span class="text-xs text-[#c6c6c6]">Set as Default</span>
									</label>
								{/if}
							</div>
							<CarbonButton
								kind="primary"
								size="md"
								class="gap-2"
								onclick={createListener}
								disabled={!newListener.name || !!portError}
							>
								<Plus class="h-4 w-4" />
								<span>Add Listener</span>
							</CarbonButton>
						</div>
					</div>
				</div>
			</div>
		</div>

		<!-- Active Routes -->
		{#if activeRoutes.length > 0}
			<div class="space-y-4 rounded-none border border-[#393939] bg-[#262626] p-5 shadow-none">
				<div class="border-b border-[#393939] pb-4">
					<h3 class="font-sans text-base font-normal text-[#f4f4f4]">Active Routes</h3>
					<p class="text-xs text-[#a8a8a8]">Servers currently using proxy routing</p>
				</div>
				<div class="space-y-2">
					{#each activeRoutes as route (route.serverId)}
						<div
							class="flex items-center justify-between rounded-none border border-[#393939] bg-[#1e1e1e] p-3"
						>
							<div class="flex items-center gap-3">
								<Activity class="h-4 w-4 {route.active ? 'text-[#42be65]' : 'text-[#8d8d8d]'}" />
								<div>
									<p class="font-mono text-xs text-[#f4f4f4]">{route.hostname}</p>
									<p class="text-xs text-[#8d8d8d]">
										Server: {route.serverId.slice(0, 8)}...
									</p>
								</div>
							</div>
							<span
								class="rounded-none border px-2 py-0.5 font-mono text-xs {route.active
									? 'border-[#24a148] bg-[#24a148]/20 text-[#42be65]'
									: 'border-[#525252] bg-transparent text-[#8d8d8d]'}"
							>
								{route.active ? 'Active' : 'Inactive'}
							</span>
						</div>
					{/each}
				</div>
			</div>
		{/if}
	{/if}
</div>
<CarbonConfirm
	bind:open={confirmOpen}
	title="Delete listener?"
	message="The proxy port stops accepting connections. Servers using it were already checked."
	confirmLabel="Delete Listener"
	danger
	onconfirm={confirmDeleteListener}
	onclose={() => (pendingDelete = null)}
>
	{#snippet details()}
		{#if pendingDelete?.listener}{pendingDelete.listener.name} · port {pendingDelete.listener
				.port}{/if}
	{/snippet}
</CarbonConfirm>
