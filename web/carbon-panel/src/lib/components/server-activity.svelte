<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { Code, ConnectError } from '@connectrpc/connect';
	import { RefreshCw } from '@lucide/svelte';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import {
		ListActivityLogsRequestSchema,
		type ActivityLogEntry
	} from '$lib/proto/carbonpanel/v1/activity_pb';
	import { CarbonButton, CarbonSelect, CarbonTag, CarbonSpinner } from '$lib/components/carbon';
	import { formatTimestamp } from '$lib/utils/format-time';
	import { activityCategory, describeActivity } from './server-activity';

	let { serverId, active = false }: { serverId: string; active?: boolean } = $props();

	const pageSize = 100;
	const maxEntries = 500;
	let history = $state<{ serverId: string; entries: ActivityLogEntry[]; limit: number }>();
	let current = $derived(history?.serverId === serverId ? history : undefined);
	let entries = $derived(current?.entries ?? []);
	let category = $state<string | number>('all');
	let displayed = $derived(
		entries.filter((entry) => category === 'all' || activityCategory(entry.event) === category)
	);
	let loading = $state(true);
	let loadError = $state('');
	let request: AbortController | undefined;

	async function loadActivity(serverId: string, limit: number) {
		request?.abort();
		const controller = new AbortController();
		request = controller;
		loading = true;
		loadError = '';
		try {
			const response = await rpcClient.activity.listActivityLogs(
				create(ListActivityLogsRequestSchema, { serverId, limit }),
				{ ...silentCallOptions, signal: controller.signal }
			);
			if (!controller.signal.aborted) history = { serverId, entries: response.entries, limit };
		} catch (error) {
			if (controller.signal.aborted) return;
			const code = ConnectError.from(error).code;
			if (
				code === Code.PermissionDenied ||
				code === Code.Unauthenticated ||
				code === Code.NotFound
			) {
				history = undefined;
				loadError =
					code === Code.NotFound
						? 'This server is no longer available.'
						: 'You don’t have access to this activity log.';
			} else {
				loadError = 'Could not load the activity log.';
			}
		} finally {
			if (!controller.signal.aborted) loading = false;
		}
	}

	$effect(() => {
		if (!active) return;
		category = 'all';
		void loadActivity(serverId, pageSize);
		return () => request?.abort();
	});
</script>

<section aria-label="Server audit log" class="border border-border bg-card">
	<div class="flex flex-wrap items-center justify-between gap-3 border-b border-border p-4">
		<h3 class="text-sm font-semibold text-foreground">Audit log</h3>
		<CarbonButton
			kind="secondary"
			size="sm"
			disabled={loading}
			onclick={() => loadActivity(serverId, current?.limit ?? pageSize)}
		>
			<RefreshCw class="mr-2 h-4 w-4" />
			{loading ? 'Loading…' : 'Refresh audit log'}
		</CarbonButton>
	</div>
	{#if loadError}
		<p role="alert" class="border-b border-border p-4 text-sm text-destructive-text">
			{loadError}
			{entries.length > 0 ? 'Showing the last loaded entries.' : ''}
		</p>
	{/if}
	{#if entries.length > 0}
		<div class="flex flex-wrap items-end justify-between gap-3 border-b border-border p-4">
			<CarbonSelect label="Action type" bind:value={category} class="w-full sm:w-48">
				<option value="all">All actions</option>
				<option value="server">Server</option>
				<option value="player">Players</option>
				<option value="mod">Mods</option>
				<option value="world">Worlds and maps</option>
				<option value="other">Other</option>
			</CarbonSelect>
			<p role="status" class="text-xs text-muted-foreground">
				{displayed.length} of {entries.length} loaded {entries.length === 1 ? 'entry' : 'entries'}
			</p>
		</div>
		{#if displayed.length === 0}
			<p class="p-6 text-sm text-muted-foreground">No loaded entries match this action type.</p>
		{:else}
			<ul aria-label="Audit entries, newest first" class="divide-y divide-border">
				{#each displayed as entry (entry.id)}
					{@const row = describeActivity(entry)}
					<li class="grid gap-2 p-4 text-sm sm:grid-cols-[minmax(0,1fr)_auto]">
						<div class="min-w-0">
							<div class="flex flex-wrap items-center gap-2">
								<p class="font-medium break-words text-foreground">{row.label}</p>
								<CarbonTag type={row.outcome.tone} size="sm">{row.outcome.label}</CarbonTag>
							</div>
							<p class="mt-1 break-words text-muted-foreground">{row.actor}</p>
							{#if row.details.length > 0}
								<details class="mt-2 text-xs">
									<summary class="focus-ring w-fit cursor-pointer text-primary">Details</summary>
									<dl class="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
										{#each row.details as [label, value] (label)}
											<dt class="text-muted-foreground">{label}</dt>
											<dd class="break-all text-foreground">{value}</dd>
										{/each}
									</dl>
								</details>
							{/if}
						</div>
						<p class="text-xs text-muted-foreground">{formatTimestamp(entry.createdAt)}</p>
					</li>
				{/each}
			</ul>
		{/if}
		{#if current && entries.length === current.limit && current.limit < maxEntries}
			<div class="border-t border-border p-4">
				<CarbonButton
					kind="tertiary"
					size="sm"
					disabled={loading}
					onclick={() => loadActivity(serverId, (current?.limit ?? pageSize) + pageSize)}
				>
					Load older entries
				</CarbonButton>
			</div>
		{/if}
	{:else if loading}
		<div class="flex items-center justify-center py-8" role="status">
			<CarbonSpinner size="md" label="Loading" description="Loading activity..." />
		</div>
	{:else if !loadError}
		<p role="status" class="p-6 text-sm text-muted-foreground">
			No audit entries have been recorded for this server.
		</p>
	{/if}
</section>
