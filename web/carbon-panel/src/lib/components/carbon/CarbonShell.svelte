<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import type { Snippet } from 'svelte';
	import { onMount } from 'svelte';
	import { fade } from 'svelte/transition';
	import { motionDuration } from '$lib/motion';
	import { page } from '$app/state';
	import { base, resolve } from '$app/paths';
	import { runningServers, activitySortedServers } from '$lib/stores/servers';
	import { authStore, currentUser, canAccessSettings } from '$lib/stores/auth';
	import CarbonModal from './CarbonModal.svelte';
	import CarbonButton from './CarbonButton.svelte';
	import CarbonTextInput from './CarbonTextInput.svelte';
	import PreferencesDialog from '../PreferencesDialog.svelte';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { type Node } from '$lib/proto/carbonpanel/v1/node_pb';
	import { describeNodeStatus, pickPrimaryNode } from '$lib/utils/node-status';
	import CarbonTag from './CarbonTag.svelte';

	interface Props {
		children?: Snippet;
	}

	let { children }: Props = $props();

	let sideNavExpanded = $state(true);
	let profileOpen = $state(false);
	let profileDialogOpen = $state(false);
	let prefsOpen = $state(false);
	let passwordOld = $state('');
	let passwordNew = $state('');
	let passwordConfirm = $state('');
	let passwordError: string | null = $state(null);
	let passwordSaving = $state(false);
	let passwordDone = $state(false);

	function toggleProfile() {
		profileOpen = !profileOpen;
	}

	function closeProfile() {
		profileOpen = false;
	}

	function openProfileDialog() {
		profileOpen = false;
		passwordError = null;
		passwordDone = false;
		profileDialogOpen = true;
	}

	function openPreferencesDialog() {
		profileOpen = false;
		prefsOpen = true;
	}

	async function handleSignOut() {
		profileOpen = false;
		await authStore.logout();
	}

	async function handleDialogPasswordChange() {
		passwordError = null;
		passwordDone = false;
		if (!passwordOld || !passwordNew) {
			passwordError = 'Enter the current and a new password.';
			return;
		}
		if (passwordNew !== passwordConfirm) {
			passwordError = 'New passwords do not match.';
			return;
		}
		if (passwordNew.length < 12) {
			passwordError = 'New password must be at least 12 characters.';
			return;
		}
		passwordSaving = true;
		try {
			await authStore.changePassword(passwordOld, passwordNew);
			passwordDone = true;
			passwordOld = '';
			passwordNew = '';
			passwordConfirm = '';
		} catch (err) {
			passwordError = err instanceof Error ? err.message : 'Failed to change password';
		} finally {
			passwordSaving = false;
		}
	}

	function handleProfileKeydown(event: KeyboardEvent) {
		if (event.key === 'Escape') {
			profileOpen = false;
		}
	}
	let servers = $derived($activitySortedServers);
	let runningCount = $derived($runningServers.length);
	let user = $derived($currentUser);
	let showSettingsNav = $derived($canAccessSettings);

	// Narrow-screen breakpoint mirrors IsMobile (768px) and Tailwind's md: prefix.
	const MOBILE_QUERY = '(max-width: 767px)';

	let isMobile = $state(false);
	let nodes = $state<Node[]>([]);
	let nodesLoaded = $state(false);
	let appPort = $state('');

	let primaryNode = $derived(pickPrimaryNode(nodes));
	let primaryNodeStatus = $derived(describeNodeStatus(primaryNode?.status));

	function applyStoredPreferences() {
		// Preferences apply on boot, not only when the dialog opens: a stored
		// reduced-motion or compact-density choice must hold on first paint.
		// Unknown values fall through to the defaults.
		try {
			const theme = localStorage.getItem('carbon-theme');
			const motion = localStorage.getItem('carbon-motion');
			const density = localStorage.getItem('carbon-density');
			if (theme === 'light' || theme === 'dark')
				document.documentElement.setAttribute('data-carbon-theme', theme);
			if (motion === 'full' || motion === 'reduced')
				document.documentElement.setAttribute('data-carbon-motion', motion);
			if (density === 'comfortable' || density === 'compact')
				document.documentElement.setAttribute('data-carbon-density', density);
		} catch {
			// Private mode: defaults stand.
		}
	}

	onMount(() => {
		applyStoredPreferences();
		const media = window.matchMedia(MOBILE_QUERY);
		const applyBreakpoint = () => {
			isMobile = media.matches;
			// Carbon behaviour: the sidenav starts hidden behind the hamburger on narrow screens.
			if (media.matches) sideNavExpanded = false;
		};
		applyBreakpoint();
		media.addEventListener('change', applyBreakpoint);

		appPort = window.location.port || (window.location.protocol === 'https:' ? '443' : '80');

		// Best-effort: the shell must render even when the node list fails (e.g. anonymous access).
		rpcClient.node
			.listNodes({}, silentCallOptions)
			.then((res) => {
				nodes = res.nodes ?? [];
			})
			.catch((err) => {
				console.error('Failed to fetch nodes for shell footer:', err);
			})
			.finally(() => {
				nodesLoaded = true;
			});

		return () => {
			media.removeEventListener('change', applyBreakpoint);
		};
	});

	function toggleNav() {
		sideNavExpanded = !sideNavExpanded;
	}

	function closeMobileNav() {
		if (isMobile) sideNavExpanded = false;
	}

	function handleWindowKeydown(event: KeyboardEvent) {
		if (event.key === 'Escape') closeMobileNav();
	}

	interface NavItem {
		href:
			| '/'
			| '/servers'
			| '/servers/new'
			| '/modpacks/studio'
			| '/modpacks'
			| '/modules'
			| '/settings'
			| '/docs/api'
			| '/changelog';
		label: string;
		icon: string;
		badge?: boolean;
	}

	const navItems: NavItem[] = [
		{
			href: '/',
			label: 'Overview',
			icon: 'M3 13h8V3H3v10zm0 8h8v-6H3v6zm10 0h8V11h-8v10zm0-18v6h8V3h-8z'
		},
		{
			href: '/servers',
			label: 'Servers',
			badge: true,
			icon: 'M4 6h16v3H4V6zm0 5h16v3H4v-3zm0 5h16v3H4v-3z'
		},
		{ href: '/servers/new', label: 'Create Server', icon: 'M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z' },
		{
			href: '/modpacks/studio',
			label: 'Modpack Studio',
			icon: 'M21 16.5l-9 5.2-9-5.2V7.5l9-5.2 9 5.2v9zM12 4.1L5 8.1l7 4 7-4-7-4zm-7 5.7v6.6l6 3.5v-6.6l-6-3.5zm8 10.1l6-3.5V9.8l-6 3.5v6.6z'
		},
		{
			href: '/modpacks',
			label: 'Modpacks',
			icon: 'M19 3H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm-5 14H7v-2h7v2zm3-4H7v-2h10v2zm0-4H7V7h10v2z'
		},
		{
			href: '/modules',
			label: 'Modules & Sidecars',
			icon: 'M20.5 11H19V7c0-1.1-.9-2-2-2h-4V3.5a2.5 2.5 0 00-5 0V5H4c-1.1 0-1.99.9-1.99 2v3.8H3.5c1.49 0 2.7 1.21 2.7 2.7s-1.21 2.7-2.7 2.7H2V20c0 1.1.9 2 2 2h3.8v-1.5c0-1.49 1.21-2.7 2.7-2.7 1.49 0 2.7 1.21 2.7 2.7V22H17c1.1 0 2-.9 2-2v-4h1.5a2.5 2.5 0 000-5z'
		},
		{
			href: '/settings',
			label: 'Settings',
			icon: 'M19.14 12.94c.04-.3.06-.61.06-.94 0-.32-.02-.64-.07-.94l2.03-1.58c.18-.14.23-.41.12-.61l-1.92-3.32c-.12-.22-.37-.29-.59-.22l-2.39.96c-.5-.38-1.03-.7-1.62-.94l-.36-2.54c-.04-.24-.24-.41-.48-.41h-3.84c-.24 0-.43.17-.47.41l-.36 2.54c-.59.24-1.13.57-1.62.94l-2.39-.96c-.22-.08-.47 0-.59.22L2.74 8.87c-.12.21-.08.47.12.61l2.03 1.58c-.05.3-.09.63-.09.94s.02.64.07.94l-2.03 1.58c-.18.14-.23.41-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.38 1.03.7 1.62.94l.36 2.54c.05.24.24.41.48.41h3.84c.24 0 .44-.17.47-.41l.36-2.54c.59-.24 1.13-.56 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32c.12-.22.07-.47-.12-.61l-2.01-1.58zM12 15.6c-1.98 0-3.6-1.62-3.6-3.6s1.62-3.6 3.6-3.6 3.6 1.62 3.6 3.6-1.62 3.6-3.6 3.6z'
		},
		{
			href: '/docs/api',
			label: 'API Reference',
			icon: 'M14 2H6c-1.1 0-1.99.9-1.99 2L4 20c0 1.1.89 2 1.99 2H18c1.1 0 2-.9 2-2V8l-6-6zm2 16H8v-2h8v2zm0-4H8v-2h8v2zm-3-5V3.5L18.5 9H13z'
		},
		{
			href: '/changelog',
			label: 'Changelog',
			icon: 'M19 3H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm-5 14H7v-2h7v2zm3-4H7v-2h10v2zm0-4H7V7h10v2z'
		}
	];

	let activeNavHref = $derived.by(() => {
		const current = page.url.pathname;
		const matching = navItems.filter((item) => {
			if (item.href === '/') return current === '/';
			return current === item.href || current.startsWith(item.href + '/');
		});
		if (matching.length === 0) return null;
		return matching.reduce((prev, curr) => (curr.href.length > prev.href.length ? curr : prev))
			.href;
	});

	function isCurrentPath(path: string): boolean {
		return activeNavHref === path;
	}
</script>

<svelte:window onkeydown={handleWindowKeydown} />

<div
	class="flex h-dvh min-h-0 flex-col bg-[#161616] font-sans text-[#f4f4f4] antialiased selection:bg-[#0f62fe] selection:text-white"
>
	<!-- IBM Carbon Global Header (48px fixed) -->
	<header
		class="fixed top-0 right-0 left-0 z-50 flex h-12 items-center justify-between border-b border-[#393939] bg-[#161616] px-3"
	>
		<div class="flex items-center gap-2">
			<!-- Hamburger Button -->
			<button
				type="button"
				onclick={toggleNav}
				aria-expanded={sideNavExpanded}
				aria-controls="carbon-sidenav"
				class="flex h-8 w-8 cursor-pointer items-center justify-center text-[#f4f4f4] transition-colors hover:bg-[#353535]"
				aria-label="Toggle Navigation"
			>
				<svg class="h-4 w-4" fill="currentColor" viewBox="0 0 20 20">
					<path
						fill-rule="evenodd"
						d="M3 5h14a1 1 0 010 2H3a1 1 0 010-2zm0 4h14a1 1 0 010 2H3a1 1 0 010-2zm0 4h14a1 1 0 010 2H3a1 1 0 010-2z"
						clip-rule="evenodd"
					/>
				</svg>
			</button>

			<!-- Brand Logo & Name -->
			<a
				href={resolve('/')}
				class="flex items-center gap-2 font-sans text-sm tracking-[0.16px] text-white transition-colors hover:text-accent-text"
			>
				<img src="{base}/brand-mark-light.svg" alt="" class="h-6 w-6" />
				<span class="font-semibold tracking-wide">Carbon Panel</span>
			</a>
		</div>

		<!-- Header Global Actions -->
		<div class="flex items-center gap-3">
			<!-- Quick Server Switcher -->
			<div class="hidden items-center gap-2 font-mono text-xs md:flex">
				<span class="text-[#8d8d8d]">RUNNING:</span>
				<CarbonTag type={runningCount > 0 ? 'green' : 'gray'} size="sm">
					{runningCount} / {servers.length}
				</CarbonTag>
			</div>

			<!-- User Profile Menu -->
			{#if user}
				<div class="relative flex items-center gap-2 border-l border-[#393939] pl-2">
					<button
						type="button"
						class="flex items-center gap-2 px-1 py-1 transition-colors hover:bg-[#262626] focus:outline-none focus-visible:outline focus-visible:outline-2 focus-visible:outline-[#0f62fe]"
						aria-haspopup="menu"
						aria-expanded={profileOpen}
						onclick={toggleProfile}
						onkeydown={handleProfileKeydown}
					>
						<div
							class="flex h-6 w-6 items-center justify-center bg-[#393939] font-mono text-xs font-bold text-[#f4f4f4]"
						>
							{user.username.slice(0, 2).toUpperCase()}
						</div>
						<span class="hidden text-xs text-[#c6c6c6] lg:inline">{user.username}</span>
						<svg
							class="h-3 w-3 text-[#8d8d8d]"
							viewBox="0 0 24 24"
							fill="currentColor"
							aria-hidden="true"
						>
							<path d="M7 10l5 5 5-5z" />
						</svg>
					</button>

					{#if profileOpen}
						<div
							class="fixed inset-0 z-40"
							role="presentation"
							onclick={closeProfile}
							onkeydown={handleProfileKeydown}
						></div>
						<div
							role="menu"
							aria-label="Profile"
							class="absolute top-full right-0 z-50 mt-1 w-44 border border-[#393939] bg-[#262626] py-1 shadow-lg"
						>
							<button
								type="button"
								role="menuitem"
								class="block w-full px-3 py-2 text-left font-sans text-sm whitespace-nowrap text-[#c6c6c6] transition-colors hover:bg-[#353535] hover:text-white"
								onclick={openProfileDialog}
							>
								Profile
							</button>
							<button
								type="button"
								role="menuitem"
								class="block w-full px-3 py-2 text-left font-sans text-sm whitespace-nowrap text-[#c6c6c6] transition-colors hover:bg-[#353535] hover:text-white"
								onclick={openPreferencesDialog}
							>
								Preferences
							</button>
							<div class="my-1 border-t border-[#393939]"></div>
							<button
								type="button"
								role="menuitem"
								class="block w-full px-3 py-2 text-left font-sans text-sm whitespace-nowrap text-[#ff8389] transition-colors hover:bg-[#353535]"
								onclick={handleSignOut}
							>
								Sign out
							</button>
						</div>
					{/if}
				</div>
			{/if}
		</div>
	</header>

	<!-- Layout Body (SideNav + Content) -->
	<div class="flex min-h-0 flex-1 pt-12">
		<!-- IBM Carbon SideNav: off-canvas overlay on narrow screens, rail/panel on md+ -->
		<aside
			id="carbon-sidenav"
			class="scrollbar-stable fixed top-12 bottom-0 left-0 z-40 w-64 overflow-x-hidden overflow-y-auto border-r border-[#393939] bg-[#161616] transition-[width,transform] duration-(--motion-fast) ease-[var(--ease-out-quart)] {sideNavExpanded
				? 'translate-x-0'
				: '-translate-x-full'} {sideNavExpanded ? 'md:w-64' : 'md:w-12'} md:translate-x-0"
		>
			<nav class="motion-stagger flex flex-col py-2" aria-label="Main Navigation">
				{#each navItems as item (item.href)}
					{#if item.href !== '/settings' || showSettingsNav}
						<a
							href={resolve(item.href)}
							onclick={closeMobileNav}
							class="relative flex items-center gap-3 py-2.5 font-sans text-sm transition-colors {sideNavExpanded
								? 'px-3.5'
								: 'justify-center px-0'} {isCurrentPath(item.href)
								? 'bg-[#353535] font-medium text-white before:absolute before:top-0 before:bottom-0 before:left-0 before:w-1 before:bg-[#0f62fe]'
								: 'text-[#c6c6c6] hover:bg-[#262626] hover:text-white'}"
							title={item.label}
						>
							<svg class="h-4 w-4 flex-shrink-0" fill="currentColor" viewBox="0 0 24 24">
								<path d={item.icon} />
							</svg>
							{#if sideNavExpanded}
								<span class="flex-1 truncate">{item.label}</span>
								{#if item.badge && runningCount > 0}
									<span
										class="flex h-4 items-center justify-center bg-[#0f62fe] px-1.5 font-mono text-xs font-semibold text-white"
									>
										{runningCount}
									</span>
								{/if}
							{/if}
						</a>
					{/if}
				{/each}
			</nav>

			<!-- SideNav Footer: live node status, links to Docker Nodes settings -->
			{#if sideNavExpanded}
				<a
					href={resolve('/settings?tab=nodes')}
					onclick={closeMobileNav}
					title="View Docker nodes"
					class="absolute right-0 bottom-0 left-0 border-t border-[#393939] bg-[#161616] p-3 font-mono text-xs text-[#8d8d8d] transition-colors hover:bg-[#262626]"
				>
					<div class="flex items-center justify-between gap-2">
						<span class="truncate"
							>{nodesLoaded
								? primaryNode
									? primaryNode.name.toUpperCase()
									: 'NO NODES'
								: '···'}</span
						>
						<span class="{primaryNodeStatus.dotClass} shrink-0 font-bold"
							>{primaryNodeStatus.label}</span
						>
					</div>
					<div class="mt-1 text-xs text-[#a8a8a8]">PORT {appPort || '···'} · CARBON V11</div>
				</a>
			{/if}
		</aside>

		<!-- Backdrop for the off-canvas sidenav on narrow screens -->
		{#if isMobile && sideNavExpanded}
			<button
				type="button"
				aria-label="Close navigation"
				onclick={closeMobileNav}
				transition:fade={{ duration: motionDuration(150) }}
				class="fixed inset-0 top-12 z-30 cursor-default bg-black/60 md:hidden"
			></button>
		{/if}

		<!-- Carbon Main Content Area -->
		<main
			class="scrollbar-stable ml-0 flex min-h-0 flex-1 flex-col overflow-y-auto p-4 transition-[margin] duration-(--motion-fast) ease-[var(--ease-out-quart)] md:p-6 {sideNavExpanded
				? 'md:ml-64'
				: 'md:ml-12'}"
		>
			<div class="mx-auto flex min-h-full w-full max-w-7xl flex-1 flex-col">
				{@render children?.()}
			</div>
		</main>
	</div>

	<CarbonModal
		bind:open={profileDialogOpen}
		title="Profile"
		description="Signed-in account summary"
		size="md"
		hasFooter={false}
		onclose={() => (profileDialogOpen = false)}
	>
		{#if user}
			<div class="space-y-3">
				<div
					class="flex items-center justify-between gap-3 rounded-none border border-[#393939] bg-[#161616] p-3"
				>
					<span class="text-xs text-[#c6c6c6]">Username</span>
					<span class="truncate font-mono text-sm font-medium text-white" title={user.username}
						>{user.username}</span
					>
				</div>
				{#if user.email}
					<div
						class="flex items-center justify-between gap-3 rounded-none border border-[#393939] bg-[#161616] p-3"
					>
						<span class="shrink-0 text-xs text-[#c6c6c6]">Email</span>
						<span class="truncate font-mono text-sm text-white" title={user.email ?? ''}
							>{user.email}</span
						>
					</div>
				{/if}
				<div
					class="flex items-center justify-between gap-3 rounded-none border border-[#393939] bg-[#161616] p-3"
				>
					<span class="shrink-0 text-xs text-[#c6c6c6]">Roles</span>
					<span
						class="truncate font-mono text-xs text-[#c6c6c6]"
						title={(user.roles ?? []).join(', ')}
						>{(user.roles ?? []).join(', ') || 'No roles'}</span
					>
				</div>
				<a
					href={resolve('/profile')}
					onclick={() => (profileDialogOpen = false)}
					class="block truncate text-sm text-accent-text hover:underline"
				>
					Manage profile, password and API tokens
				</a>
				<div class="space-y-3 border-t border-[#393939] pt-4">
					<h3 class="text-sm font-semibold text-white">Change password</h3>
					<CarbonTextInput
						revealable
						type="password"
						label="Current password"
						autocomplete="current-password"
						bind:value={passwordOld}
						disabled={passwordSaving}
					/>
					<CarbonTextInput
						revealable
						type="password"
						label="New password"
						autocomplete="new-password"
						placeholder="Minimum 12 characters"
						bind:value={passwordNew}
						disabled={passwordSaving}
					/>
					<CarbonTextInput
						revealable
						type="password"
						label="Confirm new password"
						autocomplete="new-password"
						bind:value={passwordConfirm}
						disabled={passwordSaving}
					/>
					{#if passwordError}
						<p class="text-xs text-[#ff8389]">{passwordError}</p>
					{/if}
					{#if passwordDone}
						<p class="text-xs text-[#6fdc8c]">Password changed successfully.</p>
					{/if}
					<div class="flex items-center justify-end gap-3">
						<CarbonButton
							kind="secondary"
							onclick={() => (profileDialogOpen = false)}
							class="rounded-none"
						>
							Close
						</CarbonButton>
						<CarbonButton
							kind="primary"
							onclick={handleDialogPasswordChange}
							disabled={passwordSaving}
							class="rounded-none"
						>
							{#if passwordSaving}Updating...{:else}Update password{/if}
						</CarbonButton>
					</div>
				</div>
			</div>
		{:else}
			<p class="text-sm text-[#a8a8a8]">No signed-in user.</p>
		{/if}
	</CarbonModal>

	<PreferencesDialog bind:open={prefsOpen} />
</div>
