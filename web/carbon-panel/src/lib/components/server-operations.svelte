<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { Code, ConnectError } from '@connectrpc/connect';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import {
		ListOperationsRequestSchema,
		ResolveOperationRequestSchema,
		type LifecycleOperation
	} from '$lib/proto/carbonpanel/v1/operation_pb';
	import {
		canReleaseLock,
		correlationTagType,
		countOperationsByOutcome,
		operationActionLabel,
		outcomeTagType,
		requestKeyTitle,
		requiresManualCheck
	} from './server-operations';
	import { CarbonConfirm, CarbonTag } from '$lib/components/carbon';
	import { toast } from 'svelte-sonner';
	import { formatTimestamp } from '$lib/utils/format-time';

	let { serverId, active = false }: { serverId: string; active?: boolean } = $props();

	let operations = $state<LifecycleOperation[]>([]);
	let loading = $state(false);
	let loadError = $state<string | null>(null);
	let confirmTarget = $state<LifecycleOperation | null>(null);
	let confirmOpen = $state(false);
	let resolveNote = $state('');
	let resolving = $state(false);
	let request: AbortController | undefined;

	// A running operation can finish on its own, so poll only while one exists.
	// Polling a settled journal would be pure noise.
	const hasRunning = $derived(operations.some((op) => op.outcome === 'running'));

	async function load() {
		request?.abort();
		const controller = new AbortController();
		request = controller;
		loading = true;
		loadError = null;
		try {
			const response = await rpcClient.operation.listOperations(
				create(ListOperationsRequestSchema, { serverId, limit: 50 }),
				{ ...silentCallOptions, signal: controller.signal }
			);
			if (!controller.signal.aborted) operations = response.operations;
		} catch (error) {
			if (controller.signal.aborted) return;
			const code = ConnectError.from(error).code;
			if (
				code === Code.PermissionDenied ||
				code === Code.Unauthenticated ||
				code === Code.NotFound
			) {
				operations = [];
				confirmOpen = false;
				confirmTarget = null;
			}
			loadError = error instanceof Error ? error.message : 'Unknown error';
		} finally {
			if (!controller.signal.aborted) loading = false;
		}
	}

	$effect(() => {
		if (active) {
			void load();
			return () => request?.abort();
		}
	});

	$effect(() => {
		if (!active || !hasRunning) {
			return;
		}
		const timer = setInterval(() => {
			if (!loading) void load();
		}, 5000);
		return () => clearInterval(timer);
	});

	const counts = $derived(countOperationsByOutcome(operations));

	function askResolve(op: LifecycleOperation) {
		confirmTarget = op;
		confirmOpen = true;
		resolveNote = '';
	}

	async function resolveOperation() {
		const target = confirmTarget;
		if (!target) return;
		resolving = true;
		try {
			const response = await rpcClient.operation.resolveOperation(
				create(ResolveOperationRequestSchema, {
					serverId,
					operationId: target.id,
					note: resolveNote.trim()
				})
			);
			const updated = response.operation;
			if (updated) {
				operations = operations.map((op) => (op.id === target.id ? updated : op));
			}
			toast.success('Lifecycle lock released');
			confirmOpen = false;
			confirmTarget = null;
			void load();
		} catch (error) {
			toast.error(
				'Failed to release the lock: ' + (error instanceof Error ? error.message : 'Unknown error')
			);
		} finally {
			resolving = false;
		}
	}
</script>

<div class="rounded-none border border-border bg-card p-6">
	<div class="mb-1 flex flex-wrap items-center justify-between gap-2">
		<h3 class="text-sm font-medium text-foreground">Lifecycle operations</h3>
		<button
			type="button"
			class="inline-flex h-8 items-center rounded-none bg-secondary px-4 text-xs text-foreground transition-colors hover:bg-[var(--button-secondary-hover)] disabled:opacity-50"
			onclick={load}
			disabled={loading}
		>
			{loading ? 'Loading…' : 'Refresh'}
		</button>
	</div>
	<p class="mb-4 text-xs text-muted-foreground">
		Starts, stops, restarts, deletes and migrations are journaled first. Interrupted requests show
		as unknown with the next step.
	</p>

	{#if operations.length > 0}
		<div class="mb-4 flex flex-wrap items-center gap-2" aria-label="Operations by outcome">
			{#each Object.entries(counts) as [outcome, count] (outcome)}
				<CarbonTag type={outcomeTagType(outcome)} size="sm">{count} {outcome}</CarbonTag>
			{/each}
		</div>
	{/if}

	{#if loadError}
		<div
			class="mb-4 flex flex-wrap items-center justify-between gap-2 border border-destructive bg-background p-3"
			role="alert"
		>
			<p class="text-sm text-destructive-text">
				Could not load operations. {loadError}
				{operations.length > 0 ? 'Showing the last loaded operations.' : ''}
			</p>
			<button
				type="button"
				class="inline-flex h-7 items-center rounded-none bg-secondary px-3 text-xs text-foreground hover:bg-[var(--button-secondary-hover)]"
				onclick={load}>Retry</button
			>
		</div>
	{/if}

	{#if operations.length > 0}
		<div
			class="focus-ring overflow-x-auto"
			role="region"
			aria-label="Operations table"
			tabindex="0"
		>
			<table
				class="operation-journal-table w-full min-w-[900px] text-xs"
				aria-label="Lifecycle operation journal"
			>
				<thead>
					<tr class="border-b border-border text-left text-muted-foreground">
						<th scope="col" class="py-2 pr-4 font-medium">Started</th>
						<th scope="col" class="py-2 pr-4 font-medium">Operation</th>
						<th scope="col" class="py-2 pr-4 font-medium">Node</th>
						<th scope="col" class="py-2 pr-4 font-medium">Request key</th>
						<th scope="col" class="py-2 pr-4 font-medium">Outcome</th>
						<th scope="col" class="py-2 pr-4 font-medium">What happened</th>
						<th scope="col" class="py-2 font-medium">Next step</th>
					</tr>
				</thead>
				<tbody>
					{#each operations as op (op.id)}
						<tr class="border-b border-card align-top text-foreground">
							<td class="py-2 pr-4 font-mono whitespace-nowrap text-muted-foreground"
								>{formatTimestamp(op.startedAt)}</td
							>
							<td class="py-2 pr-4 whitespace-nowrap">
								<div class="font-mono">{op.kind}</div>
								<div class="text-[11px] text-muted-foreground">{op.phase}</div>
							</td>
							<td class="py-2 pr-4 whitespace-nowrap">
								<div class="max-w-[18rem] truncate font-mono" title={op.nodeId || undefined}>
									{op.nodeId || '—'}
								</div>
								<div
									class="mt-1 flex flex-wrap items-center gap-1 text-[11px] text-muted-foreground"
								>
									<span>gen {op.endpointGeneration}</span>
									<CarbonTag type={correlationTagType(op.correlation)} size="sm"
										>{op.correlation || 'unknown'}</CarbonTag
									>
								</div>
							</td>
							<td class="max-w-[180px] truncate py-2 pr-4 font-mono" title={requestKeyTitle(op)}>
								{op.requestKey || op.requestId || '—'}
							</td>
							<td class="py-2 pr-4">
								<CarbonTag type={outcomeTagType(op.outcome)} size="sm">{op.outcome}</CarbonTag>
								{#if requiresManualCheck(op)}
									<p class="mt-1 text-[11px] font-medium text-warning-text">Do not retry blindly</p>
								{/if}
							</td>
							<td class="max-w-[380px] py-2 pr-4">
								<p class="font-mono text-[11px] text-muted-foreground">{op.reasonCode}</p>
								<p class="mt-1 text-muted-foreground">{op.reason}</p>
								{#if op.lastError}
									<p class="mt-1 font-mono text-[11px] break-words text-muted-foreground">
										{op.lastError}
									</p>
								{/if}
							</td>
							<td class="py-2">
								<div class="flex flex-col items-start gap-1">
									<span class="text-muted-foreground">{operationActionLabel(op.nextAction)}</span>
									{#if canReleaseLock(op)}
										<button
											type="button"
											class="inline-flex h-7 items-center rounded-none border border-border px-3 text-xs text-foreground transition-colors hover:bg-secondary"
											onclick={() => askResolve(op)}
										>
											Release lock
										</button>
									{/if}
								</div>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{:else if loading || !loadError}
		<p role="status" class="py-6 text-sm text-muted-foreground">
			{loading
				? 'Loading operations…'
				: 'No operations yet. Start, stop or restart to populate this journal.'}
		</p>
	{/if}
</div>

<CarbonConfirm
	bind:open={confirmOpen}
	title="Release the lifecycle lock?"
	message="Releases the lock only. Does not touch containers or world files. Release only after checking what the interrupted run did."
	confirmLabel="Release lock"
	confirming={resolving}
	danger={true}
	onconfirm={resolveOperation}
	onclose={() => {
		confirmOpen = false;
		confirmTarget = null;
	}}
>
	{#snippet details()}
		<label class="mb-1 block text-muted-foreground" for="operation-resolve-note"
			>Note (optional)</label
		>
		<input
			id="operation-resolve-note"
			class="w-full rounded-none border border-border bg-card px-2 py-1 text-foreground outline-none focus:border-primary"
			placeholder="What did you check before releasing it?"
			bind:value={resolveNote}
			maxlength="500"
		/>
	{/snippet}
</CarbonConfirm>
