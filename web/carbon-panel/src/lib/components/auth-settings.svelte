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
	import { create } from '@bufbuild/protobuf';
	import { rpcClient } from '$lib/api/rpc-client';
	import { canUpdateSettings } from '$lib/stores/auth';
	import { toast } from 'svelte-sonner';
	import { UpdateAuthSettingsRequestSchema } from '$lib/proto/carbonpanel/v1/auth_pb';
	import type { GetAuthConfigResponse } from '$lib/proto/carbonpanel/v1/auth_pb';
	import { Shield, Loader2, Check, X, Save, KeyRound, Globe } from '@lucide/svelte';
	import {
		CarbonAccordion,
		CarbonAccordionItem,
		CarbonButton,
		CarbonInlineLoading,
		CarbonTag,
		CarbonTextInput,
		CarbonTile
	} from '$lib/components/carbon';

	let loading = $state(true);
	let saving = $state(false);
	let config = $state<GetAuthConfigResponse | null>(null);

	// Editable form state
	let localAuthEnabled = $state(true);
	let allowRegistration = $state(false);
	let anonymousAccess = $state(false);
	// Text, not a controlled number: "1." must stay on screen long enough to become "1.5".
	let sessionTimeoutText = $state('24');

	let configYamlOpen = $state(false);
	let dockerComposeOpen = $state(false);
	let envVarsOpen = $state(false);

	$effect(() => {
		if (configYamlOpen) {
			dockerComposeOpen = false;
			envVarsOpen = false;
		}
	});
	$effect(() => {
		if (dockerComposeOpen) {
			configYamlOpen = false;
			envVarsOpen = false;
		}
	});
	$effect(() => {
		if (envVarsOpen) {
			configYamlOpen = false;
			dockerComposeOpen = false;
		}
	});

	let canEdit = $derived($canUpdateSettings);

	let parsedSessionHours = $derived.by(() => {
		const text = sessionTimeoutText.trim();
		if (text === '') return null;
		const hours = Number(text);
		return Number.isFinite(hours) ? hours : null;
	});
	let sessionTimeoutError = $derived(
		parsedSessionHours === null ? 'Enter the timeout in hours' : ''
	);

	let hasChanges = $derived(
		config != null &&
			(localAuthEnabled !== config.localAuthEnabled ||
				allowRegistration !== config.allowRegistration ||
				anonymousAccess !== config.anonymousAccess ||
				(parsedSessionHours !== null &&
					Math.round(parsedSessionHours * 3600) !== config.sessionTimeout))
	);

	function setSessionTimeoutFromSeconds(seconds: number) {
		const hours = Math.round((seconds / 3600) * 100) / 100;
		sessionTimeoutText = String(hours);
	}

	async function loadConfig() {
		loading = true;
		try {
			const response = await rpcClient.auth.getAuthConfig({});
			config = response;
			localAuthEnabled = response.localAuthEnabled;
			allowRegistration = response.allowRegistration;
			anonymousAccess = response.anonymousAccess;
			setSessionTimeoutFromSeconds(response.sessionTimeout);
		} catch (error) {
			console.error('Failed to load auth config:', error);
		} finally {
			loading = false;
		}
	}

	async function saveSettings() {
		if (!config) return;
		if (parsedSessionHours === null) {
			toast.error('Enter the timeout in hours');
			return;
		}
		saving = true;
		try {
			const req = create(UpdateAuthSettingsRequestSchema, {});

			if (localAuthEnabled !== config.localAuthEnabled) {
				req.localAuthEnabled = localAuthEnabled;
			}
			if (allowRegistration !== config.allowRegistration) {
				req.allowRegistration = allowRegistration;
			}
			if (anonymousAccess !== config.anonymousAccess) {
				req.anonymousAccess = anonymousAccess;
			}
			const newTimeout = Math.round(parsedSessionHours * 3600);
			if (newTimeout !== config.sessionTimeout) {
				req.sessionTimeout = newTimeout;
			}

			const response = await rpcClient.auth.updateAuthSettings(req);
			if (response.config) {
				config = response.config;
				localAuthEnabled = response.config.localAuthEnabled;
				allowRegistration = response.config.allowRegistration;
				anonymousAccess = response.config.anonymousAccess;
				setSessionTimeoutFromSeconds(response.config.sessionTimeout);
			}
			toast.success('Authentication settings updated');
		} catch (error: unknown) {
			toast.error(error instanceof Error ? error.message : 'Failed to update settings');
		} finally {
			saving = false;
		}
	}

	onMount(() => {
		loadConfig();
	});
</script>

{#if loading}
	<div class="flex items-center justify-center py-16">
		<CarbonInlineLoading description="Loading authentication settings..." />
	</div>
{:else if config}
	<div class="grid gap-6 lg:grid-cols-2">
		<CarbonTile class="rounded-none !border-[#393939] !bg-[#262626] !p-0">
			<div class="border-b border-[#393939] px-5 py-4">
				<div class="flex items-center gap-3">
					<div
						class="flex h-10 w-10 shrink-0 items-center justify-center border border-[#393939] bg-[#161616]"
					>
						<Shield class="h-5 w-5 text-[#78a9ff]" />
					</div>
					<div>
						<h2 class="font-sans text-base font-semibold text-[#f4f4f4]">
							Authentication Settings
						</h2>
						<p class="mt-0.5 font-sans text-xs text-[#8d8d8d]">
							Manage login methods and access controls
						</p>
					</div>
				</div>
			</div>

			<div class="space-y-4 p-5">
				<div
					class="flex items-center justify-between rounded-none border border-[#393939] bg-[#1e1e1e] p-3"
				>
					<div class="space-y-0.5 pr-4">
						<p class="font-sans text-sm text-[#f4f4f4]">Local Authentication</p>
						<p class="text-xs text-[#8d8d8d]">Username and password login</p>
					</div>
					{#if canEdit}
						<input
							id="local-auth-enabled"
							type="checkbox"
							class="focus-ring h-4 w-4 shrink-0 cursor-pointer accent-[#0f62fe]"
							checked={localAuthEnabled}
							onchange={(e) => {
								localAuthEnabled = e.currentTarget.checked;
								if (!localAuthEnabled) allowRegistration = false;
							}}
							disabled={saving}
							aria-label="Local authentication"
						/>
					{:else if localAuthEnabled}
						<CarbonTag type="green" size="sm">
							<Check class="mr-1 h-3 w-3" /> Enabled
						</CarbonTag>
					{:else}
						<CarbonTag type="gray" size="sm">
							<X class="mr-1 h-3 w-3" /> Disabled
						</CarbonTag>
					{/if}
				</div>

				<div
					class="flex items-center justify-between rounded-none border border-[#393939] bg-[#1e1e1e] p-3 {!localAuthEnabled
						? 'opacity-50'
						: ''}"
				>
					<div class="space-y-0.5 pr-4">
						<p class="font-sans text-sm text-[#f4f4f4]">User Registration</p>
						<p class="text-xs text-[#8d8d8d]">Allow new users to self-register</p>
					</div>
					{#if canEdit}
						<input
							id="allow-registration"
							type="checkbox"
							class="focus-ring h-4 w-4 shrink-0 cursor-pointer accent-[#0f62fe]"
							checked={allowRegistration}
							onchange={(e) => (allowRegistration = e.currentTarget.checked)}
							disabled={saving || !localAuthEnabled}
							aria-label="User registration"
						/>
					{:else if allowRegistration}
						<CarbonTag type="green" size="sm">
							<Check class="mr-1 h-3 w-3" /> Allowed
						</CarbonTag>
					{:else}
						<CarbonTag type="gray" size="sm">
							<X class="mr-1 h-3 w-3" /> Disabled
						</CarbonTag>
					{/if}
				</div>

				<div
					class="flex items-center justify-between rounded-none border border-[#393939] bg-[#1e1e1e] p-3"
				>
					<div class="space-y-0.5 pr-4">
						<p class="font-sans text-sm text-[#f4f4f4]">Anonymous Access</p>
						<p class="text-xs text-[#8d8d8d]">Limited unauthenticated browsing</p>
					</div>
					{#if canEdit}
						<input
							id="anonymous-access"
							type="checkbox"
							class="focus-ring h-4 w-4 shrink-0 cursor-pointer accent-[#0f62fe]"
							checked={anonymousAccess}
							onchange={(e) => (anonymousAccess = e.currentTarget.checked)}
							disabled={saving}
							aria-label="Anonymous access"
						/>
					{:else if anonymousAccess}
						<CarbonTag type="green" size="sm">
							<Check class="mr-1 h-3 w-3" /> Enabled
						</CarbonTag>
					{:else}
						<CarbonTag type="gray" size="sm">
							<X class="mr-1 h-3 w-3" /> Disabled
						</CarbonTag>
					{/if}
				</div>

				<div
					class="flex items-center justify-between rounded-none border border-[#393939] bg-[#1e1e1e] p-3"
				>
					<div class="space-y-0.5">
						<label for="session-timeout-hours" class="font-sans text-sm text-[#f4f4f4]"
							>Session Timeout</label
						>
						<p class="text-xs text-[#8d8d8d]">How long sessions remain valid</p>
					</div>
					{#if canEdit}
						<div class="flex items-center gap-2">
							<CarbonTextInput
								id="session-timeout-hours"
								type="number"
								min={0.084}
								step={0.5}
								class="[&_input]:h-8 [&_input]:w-24 [&_input]:bg-[#161616]"
								bind:value={sessionTimeoutText}
								error={sessionTimeoutError}
								disabled={saving}
							/>
							<span class="text-sm text-[#8d8d8d]">hours</span>
						</div>
					{:else}
						<CarbonTag type="gray" size="sm">{sessionTimeoutText}h</CarbonTag>
					{/if}
				</div>

				{#if canEdit}
					<CarbonButton
						kind="primary"
						size="md"
						class="w-full justify-center gap-2"
						onclick={saveSettings}
						disabled={saving || !hasChanges || sessionTimeoutError !== ''}
					>
						{#if saving}
							<Loader2 class="h-4 w-4 animate-spin" />
							Saving...
						{:else}
							<Save class="h-4 w-4" />
							Save Changes
						{/if}
					</CarbonButton>
				{/if}
			</div>
		</CarbonTile>

		<CarbonTile class="rounded-none !border-[#393939] !bg-[#262626] !p-0">
			<div class="border-b border-[#393939] px-5 py-4">
				<div class="flex flex-wrap items-start gap-3">
					<div
						class="flex h-10 w-10 shrink-0 items-center justify-center border border-[#393939] bg-[#161616]"
					>
						<KeyRound class="h-5 w-5 text-[#78a9ff]" />
					</div>
					<div class="min-w-0 flex-1">
						<div class="flex flex-wrap items-center gap-2">
							<h2 class="font-sans text-base font-semibold text-[#f4f4f4]">
								OIDC / Single Sign-On
							</h2>
							{#if config.oidcEnabled}
								<CarbonTag type="green" size="sm">
									<Check class="mr-1 h-3 w-3" /> Connected
								</CarbonTag>
							{/if}
						</div>
						<p class="mt-0.5 font-sans text-xs text-[#8d8d8d]">
							External identity provider integration
						</p>
					</div>
				</div>
			</div>

			<div class="space-y-4 p-5">
				{#if config.oidcEnabled}
					<div class="grid gap-3">
						{#if config.oidcIssuerUri}
							<div class="rounded-none border border-[#393939] bg-[#1e1e1e] p-3">
								<p class="text-xs text-[#8d8d8d]">Issuer URI</p>
								<p class="mt-0.5 font-mono text-sm break-all text-[#f4f4f4]">
									{config.oidcIssuerUri}
								</p>
							</div>
						{/if}

						{#if config.oidcClientId}
							<div class="rounded-none border border-[#393939] bg-[#1e1e1e] p-3">
								<p class="text-xs text-[#8d8d8d]">Client ID</p>
								<p class="mt-0.5 font-mono text-sm break-all text-[#f4f4f4]">
									{config.oidcClientId}
								</p>
							</div>
						{/if}

						{#if config.oidcRedirectUrl}
							<div class="rounded-none border border-[#393939] bg-[#1e1e1e] p-3">
								<p class="text-xs text-[#8d8d8d]">Redirect URL</p>
								<p class="mt-0.5 font-mono text-sm break-all text-[#f4f4f4]">
									{config.oidcRedirectUrl}
								</p>
							</div>
						{/if}

						{#if config.oidcScopes && config.oidcScopes.length > 0}
							<div class="rounded-none border border-[#393939] bg-[#1e1e1e] p-3">
								<p class="text-xs text-[#8d8d8d]">Scopes</p>
								<div class="mt-1 flex flex-wrap gap-1.5">
									{#each config.oidcScopes as scope (scope)}
										<CarbonTag type="gray" size="sm">{scope}</CarbonTag>
									{/each}
								</div>
							</div>
						{/if}

						{#if config.oidcRoleClaim}
							<div class="rounded-none border border-[#393939] bg-[#1e1e1e] p-3">
								<p class="text-xs text-[#8d8d8d]">Role Claim</p>
								<p class="mt-0.5 font-mono text-sm text-[#f4f4f4]">{config.oidcRoleClaim}</p>
							</div>
						{/if}
					</div>
				{:else}
					<div
						class="rounded-none border border-dashed border-[#525252] bg-[#1e1e1e] p-4 text-center"
					>
						<Globe class="mx-auto mb-2 h-8 w-8 text-[#8d8d8d]" />
						<p class="text-sm text-[#a8a8a8]">
							OIDC allows users to sign in with external identity providers like Keycloak, Authelia,
							Google, or any OpenID Connect compatible service.
						</p>
						<p class="mt-2 text-xs text-[#8d8d8d]">
							OIDC must be configured outside the UI using one of the methods below.
						</p>
					</div>

					<CarbonAccordion>
						<CarbonAccordionItem title="config.yaml" bind:open={configYamlOpen}>
							<pre
								class="overflow-x-auto rounded-none bg-[#161616] p-3 font-mono text-xs text-[#c6c6c6]"><code
									>auth:
  oidc:
    enabled: true
    issuer_uri: "https://your-provider/.well-known/openid-configuration"
    client_id: "carbon-panel"
    client_secret: "your-secret"
    redirect_url: "https://your-domain/api/v1/auth/oidc/callback"
    scopes: ["openid", "profile", "email", "groups"]
    role_claim: "groups"</code
								></pre>
						</CarbonAccordionItem>

						<CarbonAccordionItem title="Docker Compose" bind:open={dockerComposeOpen}>
							<pre
								class="overflow-x-auto rounded-none bg-[#161616] p-3 font-mono text-xs text-[#c6c6c6]"><code
									>services:
  carbon-panel:
    environment:
      CARBONPANEL_AUTH_OIDC_ENABLED: "true"
      CARBONPANEL_AUTH_OIDC_ISSUER_URI: "https://..."
      CARBONPANEL_AUTH_OIDC_CLIENT_ID: "carbon-panel"
      CARBONPANEL_AUTH_OIDC_CLIENT_SECRET: "your-secret"
      CARBONPANEL_AUTH_OIDC_REDIRECT_URL: "https://..."
      CARBONPANEL_AUTH_OIDC_SCOPES: "openid,profile,email,groups"
      CARBONPANEL_AUTH_OIDC_ROLE_CLAIM: "groups"</code
								></pre>
						</CarbonAccordionItem>

						<CarbonAccordionItem title="Environment Variables" bind:open={envVarsOpen}>
							<div class="space-y-1.5 font-mono text-xs text-[#c6c6c6]">
								<p><code class="bg-[#161616] px-1.5 py-0.5">CARBONPANEL_AUTH_OIDC_ENABLED</code></p>
								<p>
									<code class="bg-[#161616] px-1.5 py-0.5">CARBONPANEL_AUTH_OIDC_ISSUER_URI</code>
								</p>
								<p>
									<code class="bg-[#161616] px-1.5 py-0.5">CARBONPANEL_AUTH_OIDC_CLIENT_ID</code>
								</p>
								<p>
									<code class="bg-[#161616] px-1.5 py-0.5">CARBONPANEL_AUTH_OIDC_CLIENT_SECRET</code
									>
								</p>
								<p>
									<code class="bg-[#161616] px-1.5 py-0.5">CARBONPANEL_AUTH_OIDC_REDIRECT_URL</code>
								</p>
								<p><code class="bg-[#161616] px-1.5 py-0.5">CARBONPANEL_AUTH_OIDC_SCOPES</code></p>
								<p>
									<code class="bg-[#161616] px-1.5 py-0.5">CARBONPANEL_AUTH_OIDC_ROLE_CLAIM</code>
								</p>
							</div>
						</CarbonAccordionItem>
					</CarbonAccordion>

					<p class="text-xs text-[#8d8d8d]">
						Provider examples are available in the <a
							href="https://docs.carbon-panel.app/introduction/"
							target="_blank"
							rel="noopener noreferrer"
							class="text-[#78a9ff] underline hover:text-[#a6c8ff]">carbon-panel docs</a
						>.
					</p>
				{/if}
			</div>
		</CarbonTile>
	</div>
{/if}
