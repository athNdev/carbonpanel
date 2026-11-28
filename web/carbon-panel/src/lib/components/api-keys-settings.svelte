<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { onMount } from 'svelte';
	import { CarbonButton, CarbonTextInput } from '$lib/components/carbon';
	import { toast } from 'svelte-sonner';
	import {
		Key,
		CheckCircle2,
		XCircle,
		AlertCircle,
		ExternalLink,
		Loader2,
		ShieldCheck,
		Save
	} from '@lucide/svelte';
	import { rpcClient } from '$lib/api/rpc-client';

	let cfApiKey = $state('');
	let modrinthToken = $state('');
	let modrinthUserAgent = $state('');

	let loading = $state(true);
	let saving = $state(false);

	let testingCf = $state(false);
	let cfTestResult = $state<{ valid: boolean; message: string } | null>(null);

	let testingModrinth = $state(false);
	let modrinthTestResult = $state<{ valid: boolean; message: string } | null>(null);

	async function loadKeys() {
		loading = true;
		try {
			const res = await rpcClient.config.getGlobalSettings({});
			for (const cat of res.categories) {
				for (const prop of cat.properties) {
					if (prop.key === 'cfApiKey') {
						cfApiKey = prop.value || '';
					} else if (prop.key === 'modrinthToken') {
						modrinthToken = prop.value || '';
					} else if (prop.key === 'modrinthUserAgent') {
						modrinthUserAgent = prop.value || '';
					}
				}
			}
		} catch (err) {
			console.error('Failed to load API keys:', err);
			toast.error('Failed to load API keys configuration');
		} finally {
			loading = false;
		}
	}

	async function testCurseForge() {
		if (!cfApiKey.trim()) {
			toast.error('Enter a CurseForge API key first');
			return;
		}

		testingCf = true;
		cfTestResult = null;
		try {
			const res = await rpcClient.config.validateProviderKey({
				provider: 'curseforge',
				apiKey: cfApiKey.trim()
			});
			const data = { valid: res.valid, message: res.message };
			cfTestResult = data;
			if (data.valid) {
				toast.success(data.message);
			} else {
				toast.error(data.message);
			}
		} catch {
			cfTestResult = { valid: false, message: 'Failed to contact validation endpoint' };
			toast.error('Connection test failed');
		} finally {
			testingCf = false;
		}
	}

	async function testModrinth() {
		testingModrinth = true;
		modrinthTestResult = null;
		try {
			const res = await rpcClient.config.validateProviderKey({
				provider: 'modrinth',
				apiKey: modrinthToken.trim(),
				userAgent: modrinthUserAgent.trim()
			});
			const data = { valid: res.valid, message: res.message };
			modrinthTestResult = data;
			if (data.valid) {
				toast.success(data.message);
			} else {
				toast.error(data.message);
			}
		} catch {
			modrinthTestResult = { valid: false, message: 'Failed to contact validation endpoint' };
			toast.error('Connection test failed');
		} finally {
			testingModrinth = false;
		}
	}

	async function saveKeys() {
		saving = true;
		try {
			const updates: Record<string, string> = {
				cfApiKey: cfApiKey.trim(),
				modrinthToken: modrinthToken.trim(),
				modrinthUserAgent: modrinthUserAgent.trim()
			};

			await rpcClient.config.updateGlobalSettings({ updates });
			toast.success('API key settings saved');
		} catch (err) {
			console.error('Failed to save API keys:', err);
			toast.error('Failed to save API key settings');
		} finally {
			saving = false;
		}
	}

	onMount(() => {
		loadKeys();
	});
</script>

<div class="space-y-6">
	<!-- CurseForge API Key Card -->
	<div class="space-y-4 rounded-none border border-border bg-card p-5 shadow-none">
		<div class="flex items-start justify-between border-b border-border pb-4">
			<div class="space-y-1">
				<h3 class="flex items-center gap-2 text-base font-normal text-foreground">
					<Key class="h-4 w-4 text-accent-text" />
					CurseForge API Key
				</h3>
				<p class="text-xs text-muted-foreground">
					Required to search, browse, and synchronize CurseForge modpacks and mods.
				</p>
			</div>
			<div>
				{#if cfApiKey.trim()}
					<span
						class="inline-flex items-center gap-1 rounded-none border border-success bg-success/20 px-2 py-0.5 font-mono text-xs text-success-text"
					>
						<CheckCircle2 class="h-3 w-3" />
						Configured
					</span>
				{:else}
					<span
						class="inline-flex items-center gap-1 rounded-none border border-destructive bg-destructive/20 px-2 py-0.5 font-mono text-xs text-destructive-text"
					>
						<AlertCircle class="h-3 w-3" />
						Not Configured
					</span>
				{/if}
			</div>
		</div>

		<div class="space-y-4">
			<div class="rounded-none border border-border bg-card p-4 text-xs text-muted-foreground">
				<p class="font-medium text-foreground">How to get a CurseForge API Key:</p>
				<ol class="mt-2 list-inside list-decimal space-y-1">
					<li>
						Sign in to the
						<a
							href="https://console.curseforge.com/#/api-keys"
							target="_blank"
							rel="noopener noreferrer"
							class="inline-flex items-center gap-1 font-medium text-accent-text hover:underline"
						>
							CurseForge Developer Console <ExternalLink class="h-3 w-3" />
						</a>.
					</li>
					<li>Generate a new API Key for your organization or personal project.</li>
					<li>Paste the generated key below and test the connection to verify.</li>
				</ol>
			</div>

			<div class="flex flex-col gap-2 sm:flex-row sm:items-end">
				<CarbonTextInput
					id="cf-api-key"
					label="API Key"
					type="password"
					revealable
					placeholder="$2a$10$..."
					bind:value={cfApiKey}
					class="min-w-0 flex-1 font-mono [&_input]:bg-background"
				/>
				<CarbonButton
					kind="tertiary"
					size="md"
					class="shrink-0 gap-2"
					onclick={testCurseForge}
					disabled={testingCf || !cfApiKey.trim()}
				>
					{#if testingCf}
						<Loader2 class="h-4 w-4 animate-spin" />
						<span>Testing…</span>
					{:else}
						<ShieldCheck class="h-4 w-4" />
						<span>Test connection</span>
					{/if}
				</CarbonButton>
			</div>

			{#if cfTestResult}
				{#if cfTestResult.valid}
					<div
						role="status"
						class="flex gap-3 rounded-none border border-success/40 bg-success/10 p-3 text-success-text"
					>
						<CheckCircle2 class="mt-0.5 h-4 w-4 shrink-0 text-success-text" />
						<div class="space-y-0.5">
							<p class="text-xs font-medium">Connection Valid</p>
							<p class="text-xs">{cfTestResult.message}</p>
						</div>
					</div>
				{:else}
					<div
						role="alert"
						class="flex gap-3 rounded-none border border-destructive/40 bg-destructive/10 p-3 text-destructive-text"
					>
						<XCircle class="mt-0.5 h-4 w-4 shrink-0 text-destructive-text" />
						<div class="space-y-0.5">
							<p class="text-xs font-medium">Validation Failed</p>
							<p class="text-xs">{cfTestResult.message}</p>
						</div>
					</div>
				{/if}
			{/if}
		</div>
	</div>

	<!-- Modrinth Configuration Card -->
	<div class="space-y-4 rounded-none border border-border bg-card p-5 shadow-none">
		<div class="flex items-start justify-between border-b border-border pb-4">
			<div class="space-y-1">
				<h3 class="flex items-center gap-2 text-base font-normal text-foreground">
					<Key class="h-4 w-4 text-accent-text" />
					Modrinth Credentials & User-Agent
				</h3>
				<p class="text-xs text-muted-foreground">
					Configure Modrinth API authentication token and identification headers.
				</p>
			</div>
			<div>
				{#if modrinthToken.trim()}
					<span
						class="inline-flex items-center gap-1 rounded-none border border-success bg-success/20 px-2 py-0.5 font-mono text-xs text-success-text"
					>
						<CheckCircle2 class="h-3 w-3" />
						Authenticated
					</span>
				{:else}
					<span
						class="inline-flex items-center gap-1 rounded-none border border-border bg-transparent px-2 py-0.5 font-mono text-xs text-muted-foreground"
					>
						Public Access
					</span>
				{/if}
			</div>
		</div>

		<div class="space-y-4">
			<div class="rounded-none border border-border bg-card p-4 text-xs text-muted-foreground">
				<p class="font-medium text-foreground">
					Modrinth modpack and mod indexing works publicly without authentication. Supplying a
					Personal Access Token (PAT) unlocks elevated rate limits and enables accessing private or
					unlisted projects.
				</p>
				<p class="mt-1">
					Generate tokens in your
					<a
						href="https://modrinth.com/settings/pats"
						target="_blank"
						rel="noopener noreferrer"
						class="inline-flex items-center gap-1 font-medium text-accent-text hover:underline"
					>
						Modrinth PAT Settings <ExternalLink class="h-3 w-3" />
					</a>.
				</p>
			</div>

			<CarbonTextInput
				id="modrinth-token"
				label="Personal Access Token (PAT)"
				type="password"
				revealable
				placeholder="mrp_..."
				bind:value={modrinthToken}
				class="font-mono [&_input]:bg-background"
			/>

			<CarbonTextInput
				id="modrinth-ua"
				label="User-Agent Header"
				placeholder="CarbonPanel/1.0 (contact@example.com)"
				bind:value={modrinthUserAgent}
				helperText="Modrinth asks for a User-Agent that identifies the application and a contact address."
				class="font-mono [&_input]:bg-background"
			/>

			<div class="pt-2">
				<CarbonButton
					kind="tertiary"
					size="md"
					class="gap-2"
					onclick={testModrinth}
					disabled={testingModrinth}
				>
					{#if testingModrinth}
						<Loader2 class="h-4 w-4 animate-spin" />
						<span>Testing…</span>
					{:else}
						<ShieldCheck class="h-4 w-4" />
						<span>Test connection</span>
					{/if}
				</CarbonButton>
			</div>

			{#if modrinthTestResult}
				{#if modrinthTestResult.valid}
					<div
						role="status"
						class="flex gap-3 rounded-none border border-success/40 bg-success/10 p-3 text-success-text"
					>
						<CheckCircle2 class="mt-0.5 h-4 w-4 shrink-0 text-success-text" />
						<div class="space-y-0.5">
							<p class="text-xs font-medium">Connection Valid</p>
							<p class="text-xs">{modrinthTestResult.message}</p>
						</div>
					</div>
				{:else}
					<div
						role="alert"
						class="flex gap-3 rounded-none border border-destructive/40 bg-destructive/10 p-3 text-destructive-text"
					>
						<XCircle class="mt-0.5 h-4 w-4 shrink-0 text-destructive-text" />
						<div class="space-y-0.5">
							<p class="text-xs font-medium">Validation Failed</p>
							<p class="text-xs">{modrinthTestResult.message}</p>
						</div>
					</div>
				{/if}
			{/if}
		</div>
	</div>

	<!-- Actions -->
	<div class="flex justify-end gap-3">
		<CarbonButton
			kind="primary"
			size="md"
			class="justify-center gap-2 px-6"
			onclick={saveKeys}
			disabled={saving || loading}
		>
			{#if saving}
				<Loader2 class="h-4 w-4 animate-spin" />
				<span>Saving…</span>
			{:else}
				<Save class="h-4 w-4" />
				<span>Save API keys</span>
			{/if}
		</CarbonButton>
	</div>
</div>
