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
	import { goto } from '$app/navigation';
	import { asset, resolve } from '$app/paths';
	import { create } from '@bufbuild/protobuf';
	import { authStore } from '$lib/stores/auth';
	import { rpcClient } from '$lib/api/rpc-client';
	import { silentCallOptions } from '$lib/api/rpc-client';
	import { ValidateInviteRequestSchema } from '$lib/proto/carbonpanel/v1/auth_pb';
	import { CarbonButton, CarbonTextInput, CarbonTabs } from '$lib/components/carbon';
	import { toast } from 'svelte-sonner';
	import { Loader2, AlertCircle, TicketCheck, KeyRound, Shield } from '@lucide/svelte';

	let mode = $state<'login' | 'register'>('login');
	let username = $state('');
	let email = $state('');
	let password = $state('');
	let confirmPassword = $state('');
	let loading = $state(false);
	let error = $state('');
	let authStatus = $state({
		enabled: false,
		firstUserSetup: false,
		allowRegistration: false
	});
	let oidcEnabled = $state(false);
	let localAuthEnabled = $state(true);

	// Recovery state
	let showRecovery = $state(false);
	let recoveryKey = $state('');

	// Invite state
	let inviteCode = $state('');
	let inviteValid = $state(false);
	let inviteRequiresPin = $state(false);
	let inviteDescription = $state('');
	let invitePin = $state('');

	onMount(() => {
		const urlParams = new URLSearchParams(window.location.search);

		// Canonical OIDC error states from the callback. Never render raw IdP text.
		const oidcError = urlParams.get('error');
		if (oidcError) {
			error = oidcErrorMessage(oidcError);
			window.history.replaceState({}, '', '/login');
			return;
		}

		// The callback returns a one-time exchange code. The session token is
		// fetched with POST and never appears in a URL, Referer or history entry.
		const exchangeCode = urlParams.get('code');
		if (exchangeCode) {
			loading = true;
			window.history.replaceState({}, '', '/login');
			fetch('/api/v1/auth/oidc/exchange', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				credentials: 'same-origin',
				body: JSON.stringify({ code: exchangeCode })
			})
				.then(async (resp) => {
					if (!resp.ok) throw new Error('oidc_degraded');
					const body = (await resp.json()) as { token?: string };
					if (!body.token) throw new Error('oidc_degraded');
					authStore.setToken(body.token);
					return authStore.validateSession();
				})
				.then((valid) => {
					loading = false;
					if (valid) goto(resolve('/'));
					else error = 'Session validation failed. Please try again.';
				})
				.catch(() => {
					loading = false;
					error = 'SSO sign-in could not be completed. Please try again.';
				});
			return;
		}

		// Check for invite code in URL
		const invite = urlParams.get('invite');

		// If already authenticated, redirect to home
		if ($authStore.isAuthenticated) {
			goto(resolve('/'));
			return;
		}

		// Check auth status
		authStore.checkAuthStatus().then(async (status) => {
			authStatus = status;
			oidcEnabled = $authStore.oidcEnabled;
			localAuthEnabled = $authStore.localAuthEnabled;

			// If auth is disabled and not first user setup, redirect to home
			if (!status.enabled && !status.firstUserSetup) {
				goto(resolve('/'));
				return;
			}

			// If first user setup, show registration
			if (status.firstUserSetup) {
				mode = 'register';
				return;
			}

			// Validate invite code if present
			if (invite) {
				try {
					const resp = await rpcClient.auth.validateInvite(
						create(ValidateInviteRequestSchema, { code: invite }),
						silentCallOptions
					);
					if (resp.valid) {
						inviteCode = invite;
						inviteValid = true;
						inviteRequiresPin = resp.requiresPin;
						inviteDescription = resp.description;
						mode = 'register';
					}
				} catch {
					// Invalid invite, just show normal login
				}
				window.history.replaceState({}, '', '/login');
			}
		});
	});

	async function handleLogin() {
		error = '';
		loading = true;

		try {
			await authStore.login(username, password);
			toast.success('Logged in successfully');
			setTimeout(() => {
				goto(resolve('/'));
			}, 100);
		} catch (err: unknown) {
			error = err instanceof Error ? err.message : 'Login failed';
			loading = false;
		}
	}

	async function handleRegister() {
		error = '';

		if (password !== confirmPassword) {
			error = 'Passwords do not match';
			return;
		}

		if (password.length < 12) {
			error = 'Password must be at least 12 characters';
			return;
		}

		loading = true;

		try {
			await authStore.register(
				username,
				email,
				password,
				inviteValid ? inviteCode : undefined,
				inviteValid && inviteRequiresPin ? invitePin : undefined
			);
			toast.success(
				authStatus.firstUserSetup
					? 'Admin account created successfully'
					: 'Account created successfully'
			);
			setTimeout(() => {
				goto(resolve('/'));
			}, 100);
		} catch (err: unknown) {
			error = err instanceof Error ? err.message : 'Registration failed';
			loading = false;
		}
	}

	async function handleOIDCLogin() {
		try {
			const response = await (
				await import('$lib/api/rpc-client')
			).rpcClient.auth.getOIDCLoginURL({});
			if (response.loginUrl) {
				window.location.href = response.loginUrl;
			}
		} catch (err: unknown) {
			error = err instanceof Error ? err.message : 'Failed to initiate SSO login';
		}
	}

	async function handleRecovery() {
		error = '';
		loading = true;
		try {
			await authStore.useRecoveryKey(recoveryKey);
			toast.success('Panel reset to first-user setup');
			window.location.reload();
		} catch (err: unknown) {
			error = err instanceof Error ? err.message : 'Invalid recovery key';
			loading = false;
		}
	}

	function handleSubmit(e: Event) {
		e.preventDefault();

		if (mode === 'login') {
			handleLogin();
		} else {
			handleRegister();
		}
	}

	const authTabs = [
		{ id: 'login', label: 'Login' },
		{ id: 'register', label: 'Register' }
	];

	// Canonical, operator-safe messages for OIDC callback errors. Raw IdP error
	// descriptions and claim values are never rendered.
	const oidcErrorMessages: Record<string, string> = {
		invalid_state: 'The sign-in request expired or was already used. Please try again.',
		expired_login: 'The sign-in request expired. Please try again.',
		invalid_issuer: 'The identity provider did not match the configured issuer.',
		access_denied: 'Access was denied by the identity provider.',
		no_mapped_roles: 'Your account has no mapped Carbon Panel role. Ask an administrator.',
		membership_check_failed: 'Could not verify your group membership. Please try again.',
		oidc_degraded: 'SSO is temporarily unavailable. A local administrator can still sign in.',
		locked_out: 'Too many sign-in attempts. Please wait before retrying.',
		link_required: 'This identity requires an administrator to link it to an account.',
		role_conflict: 'A role conflict blocked sign-in. Ask an administrator to review role mapping.',
		account_disabled: 'This account is disabled.'
	};

	function oidcErrorMessage(code: string): string {
		return oidcErrorMessages[code] ?? 'SSO sign-in failed. Please try again.';
	}
</script>

<svelte:head>
	<title>Carbon Panel - Login</title>
</svelte:head>

<div
	class="flex min-h-screen items-center justify-center rounded-none bg-background p-4 font-sans text-foreground"
>
	<div
		class="motion-scale-in w-full max-w-md rounded-none border border-border bg-card p-5 shadow-2xl sm:p-8"
	>
		<!-- Header -->
		<div class="mb-8 text-center">
			<div class="mb-2 flex items-center justify-center gap-3">
				<img src={asset('brand-mark-light.svg')} alt="" class="h-8 w-8" />
				<h1 class="text-2xl font-semibold tracking-wide text-foreground">Carbon Panel</h1>
			</div>
			{#if authStatus.firstUserSetup}
				<p class="text-xs text-muted-foreground">
					Welcome! Create your initial administrator account.
				</p>
			{:else}
				<p class="text-xs text-muted-foreground">Sign in to manage your Minecraft servers</p>
			{/if}
		</div>

		{#if error}
			{#key error}
				<div
					class="motion-rise-in mb-6 flex items-start gap-2.5 rounded-none border-l-4 border-destructive bg-destructive/10 p-4 text-xs text-destructive-text"
				>
					<AlertCircle class="mt-0.5 h-4 w-4 shrink-0" />
					<span>{error}</span>
				</div>
			{/key}
		{/if}

		{#if authStatus.firstUserSetup}
			<!-- First User Setup Form -->
			<form onsubmit={handleSubmit} class="space-y-4">
				<CarbonTextInput
					label="Admin Username"
					autocomplete="username"
					placeholder="Choose admin username"
					bind:value={username}
					required
					disabled={loading}
				/>
				<CarbonTextInput
					type="email"
					autocomplete="email"
					label="Email (optional)"
					placeholder="admin@example.com"
					bind:value={email}
					disabled={loading}
				/>
				<CarbonTextInput
					revealable
					type="password"
					label="Password"
					autocomplete="new-password"
					placeholder="Choose a strong password (min 12 chars)"
					bind:value={password}
					required
					disabled={loading}
				/>
				<CarbonTextInput
					revealable
					type="password"
					label="Confirm Password"
					autocomplete="new-password"
					placeholder="Confirm your password"
					bind:value={confirmPassword}
					required
					disabled={loading}
				/>

				<div
					class="rounded-none border-l-4 border-primary bg-background p-3.5 text-xs text-muted-foreground"
				>
					{#if oidcEnabled}
						A local admin account is required for initial setup, even with SSO enabled. This ensures
						you always have a fallback login to manage the system if your identity provider becomes
						unavailable.
					{:else}
						This account will have full administrator permissions and access to the control panel.
					{/if}
				</div>

				<CarbonButton type="submit" class="w-full justify-center rounded-none" disabled={loading}>
					{#if loading}
						<Loader2 class="mr-2 h-4 w-4 animate-spin" />
						Creating admin account...
					{:else}
						Create Admin Account
					{/if}
				</CarbonButton>
			</form>
		{:else}
			{#if (authStatus.allowRegistration || inviteValid) && localAuthEnabled}
				<CarbonTabs tabs={authTabs} bind:selectedTab={mode} class="mb-6" />
			{/if}

			{#if mode === 'login'}
				<div class="motion-fade-in space-y-4">
					{#if localAuthEnabled}
						<form onsubmit={handleSubmit} class="space-y-4">
							<CarbonTextInput
								label="Username"
								autocomplete="username"
								placeholder="Enter your username"
								bind:value={username}
								required
								disabled={loading}
							/>
							<CarbonTextInput
								revealable
								type="password"
								label="Password"
								autocomplete="current-password"
								placeholder="Enter your password"
								bind:value={password}
								required
								disabled={loading}
							/>
							<CarbonButton
								type="submit"
								class="w-full justify-center rounded-none"
								disabled={loading}
							>
								{#if loading}
									<Loader2 class="mr-2 h-4 w-4 animate-spin" />
									Signing in...
								{:else}
									Sign In
								{/if}
							</CarbonButton>
						</form>
					{/if}

					{#if oidcEnabled}
						{#if localAuthEnabled}
							<div class="relative my-6 text-center">
								<div class="absolute inset-0 flex items-center">
									<div class="w-full border-t border-border"></div>
								</div>
								<span
									class="relative bg-card px-3 text-xs tracking-wider text-muted-foreground uppercase"
								>
									Or
								</span>
							</div>
						{/if}
						<CarbonButton
							kind={localAuthEnabled ? 'tertiary' : 'primary'}
							class="w-full justify-center rounded-none"
							onclick={handleOIDCLogin}
							disabled={loading}
						>
							<Shield class="mr-2 h-4 w-4" />
							Sign in with SSO
						</CarbonButton>
					{/if}
				</div>
			{:else}
				<!-- Registration Form -->
				<form onsubmit={handleSubmit} class="motion-fade-in space-y-4">
					{#if inviteValid && inviteDescription}
						<div
							class="flex items-center gap-2 rounded-none border-l-4 border-success bg-success/15 p-3 text-xs text-success-text"
						>
							<TicketCheck class="h-4 w-4 shrink-0" />
							<span>{inviteDescription}</span>
						</div>
					{/if}
					<CarbonTextInput
						label="Username"
						autocomplete="username"
						placeholder="Choose a username"
						bind:value={username}
						required
						disabled={loading}
					/>
					<CarbonTextInput
						type="email"
						autocomplete="email"
						label="Email (optional)"
						placeholder="your@email.com"
						bind:value={email}
						disabled={loading}
					/>
					<CarbonTextInput
						revealable
						type="password"
						label="Password"
						autocomplete="new-password"
						placeholder="Choose a password (min 12 chars)"
						bind:value={password}
						required
						disabled={loading}
					/>
					<CarbonTextInput
						revealable
						type="password"
						label="Confirm Password"
						placeholder="Confirm your password"
						bind:value={confirmPassword}
						required
						disabled={loading}
					/>
					{#if inviteValid && inviteRequiresPin}
						<CarbonTextInput
							revealable
							type="password"
							label="Invite PIN"
							autocomplete="off"
							placeholder="Enter invite PIN"
							bind:value={invitePin}
							required
							disabled={loading}
						/>
					{/if}
					<CarbonButton type="submit" class="w-full justify-center rounded-none" disabled={loading}>
						{#if loading}
							<Loader2 class="mr-2 h-4 w-4 animate-spin" />
							Creating account...
						{:else}
							Create Account
						{/if}
					</CarbonButton>
				</form>
			{/if}
		{/if}

		<!-- Recovery Drawer / Options -->
		{#if showRecovery}
			<div class="mt-6 space-y-4 border-t border-border pt-6">
				<div
					class="rounded-none border-l-4 border-destructive bg-destructive/10 p-3.5 text-xs text-destructive-text"
				>
					<div class="mb-1 font-semibold">Warning: Destructive Reset</div>
					This will reset panel authentication and delete all users, sessions, and invites. Server files
					and data are preserved.
				</div>
				<CarbonTextInput
					revealable
					type="password"
					label="Recovery Key"
					autocomplete="off"
					placeholder="Paste your emergency recovery key"
					bind:value={recoveryKey}
					disabled={loading}
				/>
				<div class="flex items-center gap-2">
					<CarbonButton
						kind="secondary"
						class="w-1/2 justify-center rounded-none"
						onclick={() => {
							showRecovery = false;
							error = '';
						}}
						disabled={loading}
					>
						Cancel
					</CarbonButton>
					<CarbonButton
						kind="danger"
						class="w-1/2 justify-center rounded-none"
						onclick={handleRecovery}
						disabled={loading || !recoveryKey}
					>
						{#if loading}
							<Loader2 class="mr-2 h-4 w-4 animate-spin" />
							Resetting...
						{:else}
							Reset Panel
						{/if}
					</CarbonButton>
				</div>
			</div>
		{:else if !authStatus.firstUserSetup}
			<div class="mt-6 border-t border-border pt-4 text-center">
				<button
					type="button"
					class="inline-flex min-h-6 cursor-pointer items-center gap-1.5 rounded-none px-2 py-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
					onclick={() => (showRecovery = true)}
				>
					<KeyRound class="h-3.5 w-3.5" />
					Forgot access? Recovery
				</button>
			</div>
		{/if}

		{#if $authStore.anonymousAccessEnabled}
			<div class="relative my-4 text-center">
				<div class="absolute inset-0 flex items-center">
					<div class="w-full border-t border-border"></div>
				</div>
				<span class="relative bg-card px-3 text-xs tracking-wider text-muted-foreground uppercase">
					Or
				</span>
			</div>
			<CarbonButton
				kind="ghost"
				class="w-full justify-center rounded-none"
				onclick={() => goto(resolve('/'))}
			>
				Continue as Guest
			</CarbonButton>
		{/if}
	</div>
</div>
