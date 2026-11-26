<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { CarbonButton, CarbonTag, CarbonTextInput } from '$lib/components/carbon';
	import CarbonModal from '$lib/components/carbon/CarbonModal.svelte';
	import { rpcClient } from '$lib/api/rpc-client';
	import { formatRpcError } from '$lib/api/rpc-error';
	import { authStore } from '$lib/stores/auth';
	import {
		SetWhitelistEntryRequestSchema,
		UndoPlayerActionRequestSchema,
		type PlayerActionResult
	} from '$lib/proto/carbonpanel/v1/player_pb';
	import { newRequestId } from './presentation';

	interface Props {
		serverId: string;
		serverName?: string;
		dialogOpen?: boolean;
		/** Called after a successful add or undo so the roster can refresh. */
		onDone?: () => void | Promise<void>;
	}

	let { serverId, serverName, onDone, dialogOpen = $bindable() }: Props = $props();

	let query = $state('');
	let busy = $state(false);
	let result = $state<PlayerActionResult | null>(null);
	let error = $state<string | null>(null);
	let undoMessage = $state<string | null>(null);

	const authState = $derived($authStore);
	const canAdd = $derived.by(() => {
		void authState;
		return authStore.hasPermission('players', 'access:write', serverId);
	});

	async function add() {
		const value = query.trim();
		if (!value || busy || !canAdd) return;
		busy = true;
		error = null;
		result = null;
		undoMessage = null;
		try {
			const response = await rpcClient.player.setWhitelistEntry(
				create(SetWhitelistEntryRequestSchema, {
					serverId,
					playerQuery: value,
					remove: false,
					requestId: newRequestId()
				})
			);
			result = response.outcome?.results?.[0] ?? null;
			if (result && !result.success) {
				error = result.message || 'The server refused the allowlist entry.';
			} else if (result?.success) {
				query = '';
				await onDone?.();
			}
		} catch (err) {
			error = formatRpcError(err, 'Could not add the allowlist entry.');
		} finally {
			busy = false;
		}
	}

	async function undo() {
		if (!result?.undoToken || busy) return;
		busy = true;
		undoMessage = null;
		try {
			const response = await rpcClient.player.undoPlayerAction(
				create(UndoPlayerActionRequestSchema, {
					serverId,
					undoToken: result.undoToken,
					requestId: newRequestId()
				})
			);
			const own = response.outcome?.results?.[0];
			undoMessage = own?.success ? 'Allowlist entry removed.' : own?.message || 'Undo failed.';
			if (own?.success) {
				result = null;
				await onDone?.();
			}
		} catch (err) {
			undoMessage = formatRpcError(err, 'Undo failed.');
		} finally {
			busy = false;
		}
	}

	function clear() {
		query = '';
		result = null;
		error = null;
		undoMessage = null;
	}
</script>

{#snippet form()}
	<section class="font-sans" aria-label="Add allowlist entry">
		<p class="text-sm leading-relaxed text-muted-foreground">
			Add a Java player before their first join. You can undo this from the action result. Bedrock
			XUID allowlists are not supported yet.
		</p>

		{#if !canAdd}
			<p class="mt-2 text-xs text-warning-text" role="note">
				You need <code>players:access:write</code> on this server to add allowlist entries.
			</p>
		{/if}

		<form
			class="mt-4 space-y-4"
			onsubmit={(event) => {
				event.preventDefault();
				void add();
			}}
		>
			<CarbonTextInput
				label="Exact UUID or username"
				placeholder="e.g. FreshName"
				bind:value={query}
				disabled={busy || !canAdd}
			/>
			<div class="flex flex-wrap justify-end gap-2">
				<CarbonButton kind="tertiary" size="sm" onclick={clear} disabled={busy || query === ''}>
					Clear
				</CarbonButton>
				<CarbonButton
					kind="primary"
					size="sm"
					type="submit"
					disabled={busy || !canAdd || query.trim() === ''}
				>
					{busy ? 'Adding…' : 'Add to allowlist'}
				</CarbonButton>
			</div>
		</form>

		<div aria-live="polite" role="status" class="space-y-2 [&>*:first-child]:mt-4">
			{#if error}
				<p class="text-xs text-destructive-text" role="alert">{error}</p>
			{/if}
			{#if result?.success}
				<div class="flex flex-wrap items-center gap-2">
					<CarbonTag type="green" size="sm">Added</CarbonTag>
					<span class="text-xs text-foreground">{result.message || 'Allowlist entry applied.'}</span
					>
					{#if result.undoSupported}
						<CarbonButton kind="tertiary" size="sm" disabled={busy} onclick={undo}
							>Undo</CarbonButton
						>
					{/if}
				</div>
			{/if}
			{#if undoMessage}
				<p class="text-xs text-muted-foreground">{undoMessage}</p>
			{/if}
		</div>
	</section>
{/snippet}

{#if dialogOpen !== undefined}
	<CarbonModal
		bind:open={dialogOpen}
		title="Add player to allowlist"
		description={serverName}
		size="lg"
		hasFooter={false}
	>
		{@render form()}
	</CarbonModal>
{:else}
	{@render form()}
{/if}
