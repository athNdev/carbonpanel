<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { User } from '@lucide/svelte';
	import { rpcClient } from '$lib/api/rpc-client';
	import {
		GetPlayerSkinRequestSchema,
		RefreshPlayerSkinRequestSchema
	} from '$lib/proto/carbonpanel/v1/player_pb';

	interface Props {
		identityKey: string;
		name: string;
		skinRef?: string;
		serverId?: string;
		size?: 'sm' | 'md' | 'lg';
		/** Offer an explicit operator-driven cache refresh. */
		refreshable?: boolean;
	}

	let {
		identityKey,
		name,
		skinRef = '',
		serverId = '',
		size = 'sm',
		refreshable = false
	}: Props = $props();

	const sizeClass = {
		sm: 'h-6 w-6',
		md: 'h-9 w-9',
		lg: 'h-16 w-16'
	};

	let url = $state<string | null>(null);
	let loading = $state(false);
	let failed = $state(false);
	let currentUrl: string | null = null;
	let generation = 0;
	let refreshing = $state(false);

	function revoke() {
		if (currentUrl) {
			URL.revokeObjectURL(currentUrl);
			currentUrl = null;
		}
	}

	async function loadFace(key: string, ref: string, scope: string, mine: number) {
		if (!key || !ref) return;
		loading = true;
		failed = false;
		try {
			const response = await rpcClient.player.getPlayerSkin(
				create(GetPlayerSkinRequestSchema, {
					identityKey: key,
					skinRef: ref,
					kind: 'face',
					// A server-scoped moderator can only read skins for that server.
					serverId: scope
				})
			);
			if (mine !== generation) return;
			const asset = response.asset;
			if (asset && asset.data.length > 0) {
				const bytes = new Uint8Array(asset.data);
				const blob = new Blob([bytes], {
					type: asset.contentType?.trim() || 'image/png'
				});
				const next = URL.createObjectURL(blob);
				revoke();
				currentUrl = next;
				url = next;
			} else {
				failed = true;
			}
		} catch {
			if (mine === generation) failed = true;
		} finally {
			if (mine === generation) loading = false;
		}
	}

	// The skin is fetched only through the same-origin RPC. The rendered src is
	// an object URL for bytes returned by that RPC — never a third-party URL.
	$effect(() => {
		const key = identityKey;
		const ref = skinRef;
		const scope = serverId;
		const mine = ++generation;
		revoke();
		url = null;
		failed = false;
		loading = false;
		if (key && ref) void loadFace(key, ref, scope, mine);
		return () => {
			generation++;
			revoke();
		};
	});

	async function refresh() {
		if (!identityKey || !serverId || refreshing) return;
		refreshing = true;
		try {
			const response = await rpcClient.player.refreshPlayerSkin(
				create(RefreshPlayerSkinRequestSchema, { identityKey, serverId })
			);
			const asset = response.asset;
			if (asset && asset.data.length > 0) {
				const bytes = new Uint8Array(asset.data);
				const blob = new Blob([bytes], {
					type: asset.contentType?.trim() || 'image/png'
				});
				const next = URL.createObjectURL(blob);
				const mine = generation;
				revoke();
				currentUrl = next;
				url = next;
				failed = false;
				// Keep the generation stable so the next prop-driven load still wins.
				generation = mine;
			}
		} catch {
			failed = true;
		} finally {
			refreshing = false;
		}
	}
</script>

<div class="inline-flex items-center gap-2">
	<div
		class="inline-flex shrink-0 items-center justify-center overflow-hidden border border-border bg-muted {sizeClass[
			size
		]}"
		title={url ? `${name} skin (face)` : `${name} skin unavailable`}
		aria-busy={loading}
		data-skin-state={failed ? 'unavailable' : url ? 'ready' : 'loading'}
	>
		{#if url}
			<img
				src={url}
				alt="{name} skin (face)"
				class="h-full w-full object-cover"
				style="image-rendering: pixelated"
			/>
		{:else}
			<User class="h-1/2 w-1/2 text-muted-foreground" aria-hidden="true" />
			<span class="sr-only">{name} skin unavailable</span>
		{/if}
	</div>
	{#if refreshable && identityKey && serverId}
		<button
			type="button"
			class="focus-ring rounded-none px-2 py-1 font-sans text-xs text-accent-text hover:bg-accent disabled:opacity-50"
			aria-label="Refresh skin for {name}"
			disabled={refreshing}
			onclick={refresh}
		>
			{refreshing ? 'Refreshing…' : 'Refresh skin'}
		</button>
	{/if}
</div>
