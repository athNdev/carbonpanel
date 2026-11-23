<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import type { Component, Snippet } from 'svelte';
	import { onMount, tick } from 'svelte';
	import { fade } from 'svelte/transition';
	import {
		Blocks,
		Boxes,
		ChevronDown,
		ChevronRight,
		CirclePlus,
		FileCode2,
		History,
		LayoutGrid,
		LibraryBig,
		Menu,
		Package,
		Server,
		Settings,
		Users
	} from '@lucide/svelte';
	import { motionDuration } from '$lib/motion';
	import { page } from '$app/state';
	import { asset, resolve } from '$app/paths';
	import { runningServers, activitySortedServers } from '$lib/stores/servers';
	import { authStore, currentUser, canAccessSettings } from '$lib/stores/auth';
	import CarbonModal from './CarbonModal.svelte';
	import CarbonButton from './CarbonButton.svelte';
	import CarbonTextInput from './CarbonTextInput.svelte';
	import PreferencesDialog from '../PreferencesDialog.svelte';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { type Node } from '$lib/proto/carbonpanel/v1/node_pb';
	import { describeNodeStatus, summarizeNodes } from '$lib/utils/node-status';
	import CarbonTag from './CarbonTag.svelte';
	import { theme } from '$lib/display-preferences.svelte';

	interface Props {
		children?: Snippet;
	}

	let { children }: Props = $props();

	let sideNavExpanded = $state(true);
	let profileOpen = $state(false);
	// Bound to the exact header menu trigger: a page-level menu or a remount
	// must never receive focus restored from these shell dialogs.
	let profileTriggerButton: HTMLButtonElement | null = $state(null);
	let profileDialogOpen = $state(false);
	let prefsOpen = $state(false);
	let passwordOld = $state('');
	let passwordNew = $state('');
	let passwordConfirm = $state('');
	let passwordError: string | null = $state(null);
	let passwordSaving = $state(false);
	let passwordDone = $state(false);

	let profileMenu = $state<HTMLDivElement>();
	async function toggleProfile() {
		profileOpen = !profileOpen;
		if (profileOpen) {
			await tick();
			profileMenu?.querySelector('button')?.focus();
		}
	}

	function closeProfile() {
		profileOpen = false;
	}

	async function restoreProfileFocus() {
		await tick();
		if (profileTriggerButton?.isConnected) profileTriggerButton.focus();
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

	async function handleProfileKeydown(event: KeyboardEvent) {
		if (event.key === 'Escape' && profileOpen) {
			event.preventDefault();
			profileOpen = false;
			await tick();
			profileTriggerButton?.focus();
			return;
		}
		if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
		event.preventDefault();
		const fromTrigger = event.currentTarget === profileTriggerButton;
		profileOpen = true;
		await tick();
		if (!profileMenu) return;
		const items = Array.from(profileMenu.querySelectorAll('button'));
		const index = items.findIndex((item) => item === document.activeElement);
		if (event.key === 'Home' || (fromTrigger && event.key === 'ArrowDown')) items[0]?.focus();
		else if (event.key === 'End' || fromTrigger) items.at(-1)?.focus();
		else
			items[(index + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus();
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
	let nodesUnavailable = $state(false);
	let fleet = $derived(summarizeNodes(nodes));

	onMount(() => {
		const media = window.matchMedia(MOBILE_QUERY);
		const applyBreakpoint = () => {
			isMobile = media.matches;
			// Carbon behaviour: the sidenav starts hidden behind the hamburger on narrow screens.
			if (media.matches) sideNavExpanded = false;
		};
		applyBreakpoint();
		media.addEventListener('change', applyBreakpoint);

		// Best-effort: the shell must render even when the node list fails (e.g. anonymous access).
		rpcClient.node
			.listNodes({}, silentCallOptions)
			.then((res) => {
				nodes = res.nodes ?? [];
			})
			.catch((err) => {
				nodesUnavailable = true;
				console.debug('Failed to fetch nodes for shell footer:', err);
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
		// Explicit destination contract. Players and World Library are owned by
		// sibling epics and are linked here so all three surfaces are reachable
		// after integration; their route files are not on this branch yet.
		href:
			| '/'
			| '/servers'
			| '/servers/new'
			| '/players'
			| '/library'
			| '/modpacks/studio'
			| '/modpacks'
			| '/modules'
			| '/settings'
			| '/docs/api'
			| '/changelog';
		label: string;
		icon: Component;
		badge?: boolean;
	}

	// One icon library for the whole navigation: lucide stroke glyphs at 1.5px,
	// matching the regular-weight labels. The previous filled Material paths
	// mixed two optical conventions in the same column.
	const navItems: NavItem[] = [
		{ href: '/', label: 'Dashboard', icon: LayoutGrid },
		{ href: '/servers', label: 'Servers', icon: Server, badge: true },
		{ href: '/servers/new', label: 'Create server', icon: CirclePlus },
		{ href: '/players', label: 'Players', icon: Users },
		{ href: '/library', label: 'World Library', icon: LibraryBig },
		{ href: '/modpacks/studio', label: 'Modpack Studio', icon: Blocks },
		{ href: '/modpacks', label: 'Modpacks', icon: Package },
		{ href: '/modules', label: 'Modules & Sidecars', icon: Boxes },
		{ href: '/settings', label: 'Settings', icon: Settings },
		{ href: '/docs/api', label: 'API Reference', icon: FileCode2 },
		{ href: '/changelog', label: 'Changelog', icon: History }
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
	class="flex h-dvh min-h-0 flex-col bg-background font-sans text-foreground antialiased selection:bg-primary selection:text-primary-foreground"
>
	<!-- IBM Carbon Global Header (48px fixed) -->
	<header
		class="fixed top-0 right-0 left-0 z-50 flex h-12 items-center justify-between border-b border-border bg-background px-3"
	>
		<a
			href="#carbon-main"
			class="focus-ring sr-only focus:not-sr-only focus:absolute focus:top-1 focus:left-2 focus:z-60 focus:bg-background focus:px-2 focus:py-1 focus:text-xs"
		>
			Skip to content
		</a>
		<div class="flex items-center gap-2">
			<!-- Hamburger Button -->
			<button
				type="button"
				onclick={toggleNav}
				aria-expanded={sideNavExpanded}
				aria-controls="carbon-sidenav"
				class="carbon-nav-toggle focus-ring motion-press flex h-8 w-8 cursor-pointer items-center justify-center text-foreground transition-colors hover:bg-accent"
				aria-label="Toggle Navigation"
			>
				<Menu class="h-4 w-4" strokeWidth={1.5} aria-hidden="true" />
			</button>

			<!-- Brand Logo & Name -->
			<a
				href={resolve('/')}
				class="focus-ring flex items-center gap-2 font-sans text-sm tracking-[0.16px] text-foreground transition-colors hover:text-accent-text"
			>
				<img
					src={asset($theme === 'dark' ? 'brand-mark-light.svg' : 'brand-mark-dark.svg')}
					alt=""
					class="h-6 w-6"
				/>
				<span class="font-semibold tracking-wide">Carbon Panel</span>
			</a>
		</div>

		<!-- Header Global Actions -->
		<div class="flex items-center gap-3">
			<!-- Quick Server Switcher -->
			<div class="hidden items-center gap-2 font-mono text-xs md:flex">
				<span class="text-muted-foreground">RUNNING:</span>
				<CarbonTag type={runningCount > 0 ? 'green' : 'gray'} size="sm">
					{runningCount} / {servers.length}
				</CarbonTag>
			</div>

			<!-- User Profile Menu -->
			{#if user}
				<div class="relative flex items-center gap-2 border-l border-border pl-2">
					<button
						type="button"
						bind:this={profileTriggerButton}
						class="focus-ring motion-press flex items-center gap-2 px-1 py-1 transition-colors hover:bg-accent"
						aria-haspopup="menu"
						aria-expanded={profileOpen}
						onclick={toggleProfile}
						onkeydown={handleProfileKeydown}
					>
						<div
							class="flex h-6 w-6 items-center justify-center bg-secondary font-mono text-xs font-bold text-foreground"
						>
							{user.username.slice(0, 2).toUpperCase()}
						</div>
						<span class="hidden text-xs text-muted-foreground lg:inline">{user.username}</span>
						<ChevronDown
							class="h-3 w-3 text-muted-foreground"
							strokeWidth={1.5}
							aria-hidden="true"
						/>
					</button>

					{#if profileOpen}
						<div
							class="fixed inset-0 z-40"
							role="presentation"
							onclick={closeProfile}
							onkeydown={handleProfileKeydown}
						></div>
						<div
							bind:this={profileMenu}
							role="menu"
							aria-label="Profile"
							onkeydown={handleProfileKeydown}
							class="absolute top-full right-0 z-50 mt-1 w-44 border border-border bg-popover py-1 shadow-[var(--carbon-shadow-overlay)]"
						>
							<button
								type="button"
								role="menuitem"
								class="block w-full px-3 py-2 text-left font-sans text-sm whitespace-nowrap text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
								onclick={openProfileDialog}
							>
								Profile
							</button>
							<button
								type="button"
								role="menuitem"
								class="block w-full px-3 py-2 text-left font-sans text-sm whitespace-nowrap text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
								onclick={openPreferencesDialog}
							>
								Preferences
							</button>
							<div class="my-1 border-t border-border"></div>
							<button
								type="button"
								role="menuitem"
								class="block w-full px-3 py-2 text-left font-sans text-sm whitespace-nowrap text-destructive-text transition-colors hover:bg-accent"
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
			inert={isMobile && !sideNavExpanded}
			class="scrollbar-stable fixed top-12 bottom-0 left-0 z-40 flex w-64 flex-col overflow-x-hidden overflow-y-auto border-r border-border bg-background transition-[width,transform] duration-(--motion-fast) ease-[var(--ease-out-quart)] {sideNavExpanded
				? 'translate-x-0'
				: '-translate-x-full'} {sideNavExpanded ? 'md:w-64' : 'md:w-12'} md:translate-x-0"
		>
			<nav class="motion-stagger flex shrink-0 flex-col py-2" aria-label="Main Navigation">
				{#each navItems as item (item.href)}
					{#if item.href !== '/settings' || showSettingsNav}
						<!-- resolve() is typed from this app's generated routes; the sibling-epic
						     destinations are explicit NavItem hrefs but their route files land
						     in separate PRs. The cast is scoped to this single call. -->
						<a
							aria-current={isCurrentPath(item.href) ? 'page' : undefined}
							href={resolve(item.href as '/')}
							onclick={closeMobileNav}
							class="focus-ring relative flex items-center gap-3 py-2.5 font-sans text-sm transition-colors {sideNavExpanded
								? 'px-3.5'
								: 'justify-center px-0'} {isCurrentPath(item.href)
								? 'bg-accent font-medium text-foreground before:absolute before:top-0 before:bottom-0 before:left-0 before:w-1 before:bg-primary'
								: 'text-muted-foreground hover:bg-card hover:text-foreground'}"
							title={item.label}
						>
							<item.icon class="h-4 w-4 flex-shrink-0" strokeWidth={1.5} aria-hidden="true" />
							{#if sideNavExpanded}
								<span class="flex-1 truncate">{item.label}</span>
								{#if item.badge && runningCount > 0}
									<span
										class="flex h-4 items-center justify-center bg-primary px-1.5 font-mono text-xs font-semibold text-primary-foreground"
									>
										{runningCount}
									</span>
								{/if}
							{/if}
						</a>
					{/if}
				{/each}
			</nav>

			{#if sideNavExpanded}
				<a
					href={resolve('/settings?tab=nodes')}
					onclick={closeMobileNav}
					title="View Docker nodes"
					class="focus-ring mt-auto flex shrink-0 items-center gap-3 border-t border-border px-3.5 py-3 text-foreground transition-colors hover:bg-card"
				>
					<Boxes class="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
					<div class="min-w-0 flex-1">
						<div class="text-sm font-medium">Docker nodes</div>
						<div class="mt-0.5 text-xs text-muted-foreground">
							{#if !nodesLoaded}Loading nodes…
							{:else if nodesUnavailable}Node status unavailable
							{:else if fleet.total === 0}No nodes registered
							{:else if fleet.total === 1}
								<span class="break-all">{nodes[0].name}</span> · {describeNodeStatus(
									nodes[0].status
								).label.toLowerCase()}
							{:else}
								<span class={fleet.offline ? 'text-warning-text' : 'text-success-text'}
									>{fleet.online} of {fleet.total} online</span
								>
							{/if}
						</div>
					</div>
					<ChevronRight class="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
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
			id="carbon-main"
			tabindex="-1"
			class="scrollbar-stable ml-0 flex min-h-0 flex-1 flex-col overflow-y-auto p-4 transition-[margin] duration-(--motion-fast) ease-[var(--ease-out-quart)] md:p-6 {sideNavExpanded
				? 'md:ml-64'
				: 'md:ml-12'}"
		>
			<div
				class="mx-auto flex min-h-full w-full max-w-7xl flex-col {page.url.pathname === '/'
					? 'shrink-0 grow'
					: 'flex-1'}"
			>
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
		onclose={() => {
			profileDialogOpen = false;
			void restoreProfileFocus();
		}}
	>
		{#if user}
			<div class="space-y-3">
				<div
					class="flex items-center justify-between gap-3 rounded-none border border-border bg-background p-3"
				>
					<span class="text-xs text-muted-foreground">Username</span>
					<span class="truncate font-mono text-sm font-medium text-foreground" title={user.username}
						>{user.username}</span
					>
				</div>
				{#if user.email}
					<div
						class="flex items-center justify-between gap-3 rounded-none border border-border bg-background p-3"
					>
						<span class="shrink-0 text-xs text-muted-foreground">Email</span>
						<span class="truncate font-mono text-sm text-foreground" title={user.email ?? ''}
							>{user.email}</span
						>
					</div>
				{/if}
				<div
					class="flex items-center justify-between gap-3 rounded-none border border-border bg-background p-3"
				>
					<span class="shrink-0 text-xs text-muted-foreground">Roles</span>
					<span
						class="truncate font-mono text-xs text-muted-foreground"
						title={(user.roles ?? []).join(', ')}
						>{(user.roles ?? []).join(', ') || 'No roles'}</span
					>
				</div>
				<a
					href={resolve('/profile')}
					onclick={() => (profileDialogOpen = false)}
					class="focus-ring block truncate text-sm text-accent-text hover:underline"
				>
					Manage profile, password and API tokens
				</a>
				<div class="space-y-3 border-t border-border pt-4">
					<h3 class="text-sm font-semibold text-foreground">Change password</h3>
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
						<p class="text-xs text-destructive-text">{passwordError}</p>
					{/if}
					{#if passwordDone}
						<p class="text-xs text-success-text">Password changed.</p>
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
							{#if passwordSaving}Updating…{:else}Update password{/if}
						</CarbonButton>
					</div>
				</div>
			</div>
		{:else}
			<p class="text-sm text-muted-foreground">No signed-in user.</p>
		{/if}
	</CarbonModal>

	<PreferencesDialog bind:open={prefsOpen} onclose={() => void restoreProfileFocus()} />
</div>
