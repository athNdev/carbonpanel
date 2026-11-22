<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { toast } from 'svelte-sonner';
	import { authStore } from '$lib/stores/auth';
	import { rpcClient } from '$lib/api/rpc-client';
	import { formatRpcError } from '$lib/api/rpc-error';
	import {
		CarbonButton,
		CarbonConfirm,
		CarbonDataTable,
		CarbonTag,
		CarbonTextInput
	} from '$lib/components/carbon';
	import {
		BanPlayerRequestSchema,
		BulkPlayerActionRequestSchema,
		KickPlayerRequestSchema,
		PardonPlayerRequestSchema,
		PlayerActionResultSchema,
		SetOperatorRequestSchema,
		SetWhitelistEntryRequestSchema,
		UndoPlayerActionRequestSchema,
		type PlayerActionResult
	} from '$lib/proto/carbonpanel/v1/player_pb';
	import {
		actionIsDestructive,
		actionLabel,
		actionSideEffectNote,
		bulkKindFor,
		canUndoResult,
		newRequestId,
		permissionActionFor,
		previewTargets,
		undoUnavailableReason,
		type PlayerActionKind
	} from './presentation';

	export interface PlayerActionTarget {
		identityKey: string;
		name: string;
	}

	interface Props {
		serverId: string;
		targets: PlayerActionTarget[];
		action: PlayerActionKind;
		/** Called after a successful action or undo so the host can refresh. */
		onDone?: () => void | Promise<void>;
		/**
		 * When set, the action cannot apply at all (for example a player with
		 * no associated server). The button is disabled and the reason is shown
		 * as a tooltip instead of sending an RPC that must fail.
		 */
		unavailableReason?: string;
	}

	let { serverId, targets, action, onDone, unavailableReason = '' }: Props = $props();

	const unavailable = $derived(unavailableReason.trim() !== '');

	let open = $state(false);
	let reason = $state('');
	let busy = $state(false);
	let results = $state<PlayerActionResult[]>([]);
	let errorMessage = $state<string | null>(null);
	let outcomeTrace = $state<string>('');
	let undoMessage = $state<string>('');

	// Reference the store value so this derived permission check is reactive.
	// The backend remains authoritative; this only hides controls the operator
	// cannot use.
	const authState = $derived($authStore);
	const isBulk = $derived(targets.length > 1);
	const canAct = $derived.by(() => {
		void authState;
		if (isBulk) return authStore.hasPermission('players', 'bulk', serverId);
		return authStore.hasPermission('players', permissionActionFor(action), serverId);
	});
	const undoable = $derived(results.filter((result) => canUndoResult(result)));

	async function singleResult(
		target: PlayerActionTarget,
		requestId: string
	): Promise<PlayerActionResult> {
		let outcome;
		switch (action) {
			case 'kick':
				outcome = (
					await rpcClient.player.kickPlayer(
						create(KickPlayerRequestSchema, {
							serverId,
							identityKey: target.identityKey,
							playerName: target.name,
							reason,
							requestId
						})
					)
				).outcome;
				break;
			case 'ban':
				outcome = (
					await rpcClient.player.banPlayer(
						create(BanPlayerRequestSchema, {
							serverId,
							identityKey: target.identityKey,
							playerName: target.name,
							reason,
							requestId
						})
					)
				).outcome;
				break;
			case 'pardon':
				outcome = (
					await rpcClient.player.pardonPlayer(
						create(PardonPlayerRequestSchema, {
							serverId,
							identityKey: target.identityKey,
							playerName: target.name,
							requestId
						})
					)
				).outcome;
				break;
			case 'op':
				outcome = (
					await rpcClient.player.setOperator(
						create(SetOperatorRequestSchema, {
							serverId,
							identityKey: target.identityKey,
							playerName: target.name,
							remove: false,
							level: 4,
							requestId
						})
					)
				).outcome;
				break;
			case 'deop':
				outcome = (
					await rpcClient.player.setOperator(
						create(SetOperatorRequestSchema, {
							serverId,
							identityKey: target.identityKey,
							playerName: target.name,
							remove: true,
							level: 0,
							requestId
						})
					)
				).outcome;
				break;
			case 'whitelist_add':
				outcome = (
					await rpcClient.player.setWhitelistEntry(
						create(SetWhitelistEntryRequestSchema, {
							serverId,
							identityKey: target.identityKey,
							playerName: target.name,
							remove: false,
							requestId
						})
					)
				).outcome;
				break;
			case 'whitelist_remove':
				outcome = (
					await rpcClient.player.setWhitelistEntry(
						create(SetWhitelistEntryRequestSchema, {
							serverId,
							identityKey: target.identityKey,
							playerName: target.name,
							remove: true,
							requestId
						})
					)
				).outcome;
				break;
		}
		const reported =
			outcome?.results?.find((r) => r.identityKey === target.identityKey) ?? outcome?.results?.[0];
		return (
			reported ??
			create(PlayerActionResultSchema, {
				identityKey: target.identityKey,
				success: false,
				message: 'The backend did not report a per-target result.',
				errorCode: 'NO_RESULT'
			})
		);
	}

	async function perform() {
		if (!canAct || busy || targets.length === 0 || unavailable) return;
		busy = true;
		errorMessage = null;
		undoMessage = '';
		results = [];
		outcomeTrace = '';
		const requestId = newRequestId();
		try {
			const bulkKind = isBulk ? bulkKindFor(action) : null;
			if (bulkKind) {
				const response = await rpcClient.player.bulkPlayerAction(
					create(BulkPlayerActionRequestSchema, {
						serverId,
						kind: bulkKind,
						identityKeys: targets.map((t) => t.identityKey),
						reason,
						requestId
					})
				);
				results = response.outcome?.results ?? [];
				outcomeTrace = 'Per-player results.';
			} else {
				const collected: PlayerActionResult[] = [];
				for (const target of targets) collected.push(await singleResult(target, requestId));
				results = collected;
				outcomeTrace = 'Per-player results.';
			}
			const failed = results.filter((r) => !r.success);
			if (failed.length === 0) toast.success(`${actionLabel(action)} completed`);
			else toast.error(`${actionLabel(action)}: ${failed.length} target(s) failed`);
			await onDone?.();
		} catch (error) {
			errorMessage = formatRpcError(
				error,
				`Could not ${actionLabel(action).toLowerCase()} player(s).`
			);
			toast.error(errorMessage);
		} finally {
			busy = false;
		}
	}

	async function undo() {
		if (busy || undoable.length === 0) return;
		busy = true;
		undoMessage = '';
		try {
			const messages: string[] = [];
			let allSucceeded = true;
			for (const result of undoable) {
				const response = await rpcClient.player.undoPlayerAction(
					create(UndoPlayerActionRequestSchema, {
						serverId,
						undoToken: result.undoToken,
						requestId: newRequestId()
					})
				);
				const outcome = response.outcome;
				const own = outcome?.results?.[0];
				if (!own?.success) allSucceeded = false;
				messages.push(
					own?.success
						? `Undone for ${result.identityKey}`
						: own?.message || `Undo reported no result for ${result.identityKey}`
				);
			}
			undoMessage = messages.join(' · ');
			if (allSucceeded) {
				toast.success('Undo applied');
			} else {
				toast.error(undoMessage);
			}
			await onDone?.();
		} catch (error) {
			undoMessage = formatRpcError(error, 'Undo failed. Check the audit history.');
			toast.error(undoMessage);
		} finally {
			busy = false;
		}
	}
</script>

<div class="space-y-2 font-sans">
	{#if !canAct}
		<p class="text-xs text-warning-text" role="note">
			You can't {actionLabel(action).toLowerCase()} players on this server.
		</p>
	{/if}

	<CarbonButton
		kind={actionIsDestructive(action) ? 'danger' : 'secondary'}
		size="sm"
		disabled={!canAct || targets.length === 0 || unavailable}
		title={unavailable ? unavailableReason.trim() : undefined}
		onclick={() => {
			reason = '';
			results = [];
			errorMessage = null;
			undoMessage = '';
			open = true;
		}}
	>
		{actionLabel(action)}{isBulk ? ` (${targets.length})` : ''}
	</CarbonButton>

	<CarbonConfirm
		bind:open
		title="{actionLabel(action)} {isBulk
			? `${targets.length} players`
			: (targets[0]?.name ?? 'player')}?"
		message={actionSideEffectNote(action)}
		confirmLabel={actionLabel(action)}
		danger={actionIsDestructive(action)}
		confirming={busy}
		onconfirm={perform}
		onclose={() => (open = false)}
	>
		{#snippet details()}
			<div class="space-y-3">
				<div>
					<p class="mono-label">Targets</p>
					<p class="mt-1 font-mono text-foreground">{previewTargets(targets)}</p>
				</div>
				<CarbonTextInput
					label="Reason (optional)"
					placeholder="Recorded in the audit history"
					bind:value={reason}
					disabled={busy}
				/>
				<p class="text-xs text-muted-foreground">
					{actionSideEffectNote(action)}
				</p>
				{#if !canAct}
					<p class="text-xs text-warning-text" role="note">
						Your current grants do not include this action; confirming will be rejected.
					</p>
				{/if}
			</div>
		{/snippet}
	</CarbonConfirm>

	<div aria-live="polite" class="space-y-2">
		{#if errorMessage}
			<p class="text-xs text-destructive-text" role="alert">{errorMessage}</p>
		{/if}

		{#if results.length > 0}
			<CarbonDataTable title="Action results" description={outcomeTrace}>
				{#snippet header()}
					<tr>
						<th scope="col" class="px-3 py-2">Player</th>
						<th scope="col" class="px-3 py-2">Outcome</th>
						<th scope="col" class="px-3 py-2">Detail</th>
					</tr>
				{/snippet}
				{#each results as result (result.identityKey + (result.errorCode || ''))}
					<tr>
						<td class="px-3 py-2 font-mono text-xs">{result.identityKey}</td>
						<td class="px-3 py-2">
							<CarbonTag type={result.success ? 'green' : 'red'} size="sm">
								{result.success ? 'Succeeded' : 'Failed'}
							</CarbonTag>
						</td>
						<td class="px-3 py-2 text-xs text-muted-foreground">
							{result.message || result.errorCode || 'No detail reported'}
						</td>
					</tr>
				{/each}
			</CarbonDataTable>

			<div class="flex flex-wrap items-center gap-2">
				{#if undoable.length > 0}
					<CarbonButton kind="secondary" size="sm" disabled={busy} onclick={undo}>
						Undo ({undoable.length})
					</CarbonButton>
				{:else}
					<span class="text-xs text-muted-foreground" role="note">
						Automatic undo isn't available here. {undoUnavailableReason(action)}
					</span>
				{/if}
				{#if undoMessage}
					<span class="text-xs text-foreground">{undoMessage}</span>
				{/if}
			</div>
		{/if}
	</div>
</div>
