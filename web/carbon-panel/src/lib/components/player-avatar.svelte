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
	import { CarbonButton } from '$lib/components/carbon';
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
		/**
		 * When false the server reported no resolvable skin for this identity,
		 * so no request is sent at all: the silhouette is shown without
		 * caching a placeholder as if the player had no skin.
		 */
		skinAvailable?: boolean;
		/** Also render the full 64x64 skin texture next to the face. */
		showFullSkin?: boolean;
	}

	let {
		identityKey,
		name,
		skinRef = '',
		serverId = '',
		size = 'sm',
		refreshable = false,
		skinAvailable = true,
		showFullSkin = false
	}: Props = $props();

	const sizeClass = {
		sm: 'h-6 w-6',
		md: 'h-9 w-9',
		lg: 'h-16 w-16'
	};

	// The 64x64 texture is shown at four times the face's edge so individual
	// skin pixels stay crisp instead of being smoothed away.
	const fullSizeClass = {
		sm: 'h-24 w-24',
		md: 'h-36 w-36',
		lg: 'h-64 w-64'
	};

	let url = $state<string | null>(null);
	let fullUrl = $state<string | null>(null);
	let loading = $state(false);
	let failed = $state(false);
	let currentUrl: string | null = null;
	let currentFullUrl: string | null = null;
	let generation = 0;
	let refreshing = $state(false);

	function revoke() {
		if (currentUrl) {
			URL.revokeObjectURL(currentUrl);
			currentUrl = null;
		}
		if (currentFullUrl) {
			URL.revokeObjectURL(currentFullUrl);
			currentFullUrl = null;
		}
	}

	function toObjectURL(data: Uint8Array, contentType: string | undefined): string {
		// Copying into a fresh ArrayBuffer-backed view satisfies Blob's part
		// type even when the RPC bytes use a wider buffer type.
		const bytes = new Uint8Array(data);
		const blob = new Blob([bytes], { type: contentType?.trim() || 'image/png' });
		return URL.createObjectURL(blob);
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
				const next = toObjectURL(new Uint8Array(asset.data), asset.contentType);
				if (currentUrl) URL.revokeObjectURL(currentUrl);
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

	// The full texture is supplementary: a failure leaves the face intact and
	// never marks the avatar failed.
	async function loadFullSkin(key: string, ref: string, scope: string, mine: number) {
		if (!key || !ref) return;
		try {
			const response = await rpcClient.player.getPlayerSkin(
				create(GetPlayerSkinRequestSchema, {
					identityKey: key,
					skinRef: ref,
					kind: 'skin',
					serverId: scope
				})
			);
			if (mine !== generation) return;
			const asset = response.asset;
			if (asset && asset.data.length > 0) {
				const next = toObjectURL(new Uint8Array(asset.data), asset.contentType);
				if (currentFullUrl) URL.revokeObjectURL(currentFullUrl);
				currentFullUrl = next;
				fullUrl = next;
			}
		} catch {
			// Supplementary asset: absence is rendered as no full-skin image.
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
		fullUrl = null;
		failed = false;
		loading = false;
		if (key && ref && skinAvailable) {
			loading = true;
			void loadFace(key, ref, scope, mine);
			if (showFullSkin) void loadFullSkin(key, ref, scope, mine);
		} else if (key && ref) {
			// The server reported no resolvable skin: show unavailable instead
			// of sending a request that can only come back as a placeholder.
			failed = true;
		}
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
				const next = toObjectURL(new Uint8Array(asset.data), asset.contentType);
				const mine = generation;
				if (currentUrl) URL.revokeObjectURL(currentUrl);
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
	{#if showFullSkin && fullUrl}
		<img
			src={fullUrl}
			alt="{name} full skin texture"
			class="{fullSizeClass[size]} shrink-0 border border-border bg-muted"
			style="image-rendering: pixelated"
		/>
	{/if}
	{#if refreshable && identityKey && serverId}
		<CarbonButton
			kind="tertiary"
			size="sm"
			aria-label="Refresh skin for {name}"
			loading={refreshing}
			disabled={refreshing}
			onclick={refresh}
		>
			Refresh skin
		</CarbonButton>
	{/if}
</div>
