<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { rpcClient } from '$lib/api/rpc-client';
	import type { Server } from '$lib/proto/carbonpanel/v1/common_pb';
	import {
		ListOperationsRequestSchema,
		ResolveOperationRequestSchema,
		type LifecycleOperation
	} from '$lib/proto/carbonpanel/v1/operation_pb';
	import {
		canReleaseLock,
		countOperationsByOutcome,
		operationActionLabel,
		outcomeTagType
	} from './server-operations';
	import { CarbonConfirm, CarbonTag } from '$lib/components/carbon';
	import { toast } from 'svelte-sonner';

	let { server, active = false }: { server: Server; active?: boolean } = $props();

	let operations = $state<LifecycleOperation[]>([]);
	let loading = $state(false);
	let loadError = $state<string | null>(null);
	let confirmTarget = $state<LifecycleOperation | null>(null);
	let confirmOpen = $state(false);
	let resolveNote = $state('');
	let resolving = $state(false);

	// A running operation can finish on its own, so poll only while one exists.
	// Polling a settled journal would be pure noise.
	const hasRunning = $derived(operations.some((op) => op.outcome === 'running'));

	async function load() {
		loading = true;
		loadError = null;
		try {
			const response = await rpcClient.operation.listOperations(
				create(ListOperationsRequestSchema, { serverId: server.id, limit: 50 })
			);
			operations = response.operations;
		} catch (error) {
			// Keep whatever is already on screen: a dropped connection must not
			// erase the evidence an operator was reading.
			loadError = error instanceof Error ? error.message : 'Unknown error';
		} finally {
			loading = false;
		}
	}

	$effect(() => {
		if (active) {
			void load();
		}
	});

	$effect(() => {
		if (!active || !hasRunning) {
			return;
		}
		const timer = setInterval(() => void load(), 5000);
		return () => clearInterval(timer);
	});

	function formatTime(ts: unknown): string {
		if (!ts) return '—';
		try {
			const d = (ts as { toDate?: () => Date })?.toDate?.() ?? new Date(ts as string);
			return d.toLocaleString();
		} catch {
			return '—';
		}
	}

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
					serverId: server.id,
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

<div class="rounded-none border border-[#393939] bg-[#262626] p-6">
	<div class="mb-1 flex flex-wrap items-center justify-between gap-2">
		<h3 class="text-base font-semibold text-[#f4f4f4]">Lifecycle operations</h3>
		<button
			type="button"
			class="inline-flex h-8 items-center rounded-none bg-[#393939] px-4 text-xs text-white transition-colors hover:bg-[#4c4c4c] disabled:opacity-50"
			onclick={load}
			disabled={loading}
		>
			{loading ? 'Loading…' : 'Refresh'}
		</button>
	</div>
	<p class="mb-4 text-xs text-[#a8a8a8]">
		Every start, stop, restart, delete and migrate is written down before it is attempted. An
		interrupted request shows as <span class="text-[#f1c21b]">unknown</span> — never as a success or
		a failure — with the safe next step beside it.
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
			class="mb-4 flex flex-wrap items-center justify-between gap-2 border border-[#da1e28] bg-[#161616] p-3"
			role="alert"
		>
			<p class="font-mono text-xs text-[#ff8389]">Could not load operations: {loadError}</p>
			<button
				type="button"
				class="inline-flex h-7 items-center rounded-none bg-[#393939] px-3 text-xs text-white hover:bg-[#4c4c4c]"
				onclick={load}>Retry</button
			>
		</div>
	{/if}

	{#if operations.length === 0}
		<p class="py-6 text-center font-mono text-xs text-[#a8a8a8]">
			{loading
				? 'Loading operations…'
				: 'No lifecycle operations recorded. Start, stop or restart the server to populate this journal.'}
		</p>
	{:else}
		<div class="overflow-x-auto">
			<table class="w-full min-w-[720px] text-xs" aria-label="Lifecycle operation journal">
				<thead>
					<tr class="border-b border-[#393939] text-left text-[#a8a8a8]">
						<th scope="col" class="py-2 pr-4 font-medium">Started</th>
						<th scope="col" class="py-2 pr-4 font-medium">Operation</th>
						<th scope="col" class="py-2 pr-4 font-medium">Outcome</th>
						<th scope="col" class="py-2 pr-4 font-medium">What happened</th>
						<th scope="col" class="py-2 font-medium">Next step</th>
					</tr>
				</thead>
				<tbody>
					{#each operations as op (op.id)}
						<tr class="border-b border-[#262626] align-top text-[#f4f4f4]">
							<td class="whitespace-nowrap py-2 pr-4 font-mono text-[#c6c6c6]"
								>{formatTime(op.startedAt)}</td
							>
							<td class="whitespace-nowrap py-2 pr-4 font-mono">{op.kind}</td>
							<td class="py-2 pr-4">
								<CarbonTag type={outcomeTagType(op.outcome)} size="sm">{op.outcome}</CarbonTag>
							</td>
							<td class="max-w-[380px] py-2 pr-4">
								<p class="text-[#c6c6c6]">{op.reason}</p>
								{#if op.lastError}
									<p class="mt-1 break-words font-mono text-[11px] text-[#a8a8a8]">
										{op.lastError}
									</p>
								{/if}
							</td>
							<td class="py-2">
								<div class="flex flex-col items-start gap-1">
									<span class="text-[#c6c6c6]">{operationActionLabel(op.nextAction)}</span>
	{#if canReleaseLock(op)}
										<button
											type="button"
											class="inline-flex h-7 items-center rounded-none border border-[#393939] px-3 text-xs text-white transition-colors hover:bg-[#393939]"
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
	{/if}
</div>

<CarbonConfirm
	bind:open={confirmOpen}
	title="Release the lifecycle lock?"
	message="This releases the lock on the server so it can be operated on again. It does not stop, start, remove or prune any container, and it does not touch world files. Only do this once you know what the interrupted operation actually did."
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
		<label class="mb-1 block text-[#c6c6c6]" for="operation-resolve-note">Note (optional)</label>
		<input
			id="operation-resolve-note"
			class="w-full rounded-none border border-[#393939] bg-[#262626] px-2 py-1 text-[#f4f4f4] outline-none focus:border-[#0f62fe]"
			placeholder="What did you check before releasing it?"
			bind:value={resolveNote}
			maxlength="500"
		/>
	{/snippet}
</CarbonConfirm>
