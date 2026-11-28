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

	import { asset } from '$app/paths';
	import { CarbonBreadcrumbs, CarbonTag, CarbonPageHeader } from '$lib/components/carbon';
	import { theme as preferenceTheme } from '$lib/display-preferences.svelte';

	let isLoading = $state(true);
	let loadingProgress = $state(10);
	let iframeElement: HTMLIFrameElement | null = $state(null);
	// Scalar renders a standalone document, so it cannot inherit the app's CSS
	// variables. Build its chrome from the active display preference instead of
	// hardcoding the dark palette in the light theme.
	const scalarFrame = (dark: boolean) =>
		`
			<!DOCTYPE html>
			<html>
			<head>
				<meta charset="utf-8">
				<meta name="viewport" content="width=device-width, initial-scale=1">
				<script src="` +
		asset('scalar.js') +
		`">${'<'}/script>
        <style>
          /* Hide "Powered by Scalar" link... */
          a[href="https://www.scalar.com"] {
            display: none !important;
          }
          html, body {
            background: ${dark ? '#161616' : '#ffffff'};
            color-scheme: ${dark ? 'dark' : 'light'};
          }
          /* Style scrollbar to match app */
          ::-webkit-scrollbar {
            width: 8px;
          }
          ::-webkit-scrollbar-track {
            background: transparent;
          }
          ::-webkit-scrollbar-thumb {
            background: hsl(0 0% 50% / 0.3);
            border-radius: 4px;
          }
          ::-webkit-scrollbar-thumb:hover {
            background: hsl(0 0% 50% / 0.5);
          }
        </style>
			</head>
			<body style="margin: 0; padding: 0; background: ${dark ? '#161616' : '#ffffff'};">
				<div id="api-reference"></div>
				<script>
					window.addEventListener('load', () => {
						window.parent.postMessage({ type: 'scalar-progress', value: 50 }, '*');
						window.Scalar.createApiReference('#api-reference', {
							url: '/api/v1/openapi.yaml',
							darkMode: ${dark},
							hideClientButton: true,
              showDeveloperTools: 'never',
              showToolbar: 'never'
						});
						window.parent.postMessage({ type: 'scalar-loaded' }, '*');
					});
				${'<'}/script>
			</body>
			</html>
		`;

	// Scalar is a standalone document and cannot inherit CSS variables. Rewrite
	// it when the display preference changes so a live theme flip does not leave
	// the embedded reference in the previous palette (the outer frame follows
	// --background immediately, which would otherwise be a visible mismatch).
	let frameTheme: boolean | null = null;
	$effect(() => {
		const dark = $preferenceTheme !== 'light';
		if (!iframeElement || frameTheme === dark) return;
		const doc = iframeElement.contentDocument;
		if (!doc) return;
		frameTheme = dark;
		doc.open();
		doc.write(scalarFrame(dark));
		doc.close();
	});

	onMount(() => {
		// Simulate progress, but gets overridden by actual load state.
		const progressInterval = setInterval(() => {
			if (loadingProgress < 90) {
				loadingProgress += 10;
			}
		}, 200);

		// Post-load timer: cleared on unmount so it never writes state after teardown.
		let loadTimer: ReturnType<typeof setTimeout> | undefined;

		// Listen for load confirmation
		const handleMessage = (e: MessageEvent) => {
			if (e.data?.type === 'scalar-progress') {
				loadingProgress = e.data.value;
			} else if (e.data?.type === 'scalar-loaded') {
				clearInterval(progressInterval);
				loadingProgress = 100;

				// Small delay for progress, makes transition smoother
				loadTimer = setTimeout(() => {
					isLoading = false;
				}, 300);
				window.removeEventListener('message', handleMessage);
			}
		};
		window.addEventListener('message', handleMessage);

		// Cleanup on unmount
		return () => {
			clearInterval(progressInterval);
			if (loadTimer) clearTimeout(loadTimer);
			window.removeEventListener('message', handleMessage);
		};
	});

	// ConnectRPC service catalog. Method counts verified against
	// proto/carbonpanel/v1/*.proto (package carbonpanel.v1, 155 methods total).
	// The Scalar viewer below renders the live spec served from
	// /api/v1/openapi.yaml (regenerated via `make gen`), so this catalog only
	// mirrors service names + procedure roots, never method detail.
	const connectServices: { name: string; methods: number; blurb: string }[] = [
		{ name: 'AuthService', methods: 18, blurb: 'Session, OIDC, API keys' },
		{ name: 'ServerService', methods: 15, blurb: 'Lifecycle, migrate, console' },
		{ name: 'ModpackService', methods: 15, blurb: 'Index, import, versions' },
		{ name: 'ModuleService', methods: 18, blurb: 'Sidecars, lifecycle hooks' },
		{ name: 'FileService', methods: 16, blurb: 'Browse, edit, transfer' },
		{ name: 'TaskService', methods: 12, blurb: 'Schedules, runs' },
		{ name: 'ProxyService', methods: 12, blurb: 'Routing, virtual hosts' },
		{ name: 'RoleService', methods: 10, blurb: 'RBAC roles' },
		{ name: 'ModService', methods: 7, blurb: 'Mod install, versions' },
		{ name: 'NodeService', methods: 6, blurb: 'Docker nodes, placement' },
		{ name: 'ConfigService', methods: 8, blurb: 'Settings, config, credential validation' },
		{ name: 'UserService', methods: 5, blurb: 'Users, profiles' },
		{ name: 'SupportService', methods: 4, blurb: 'Diagnostics, bundles' },
		{ name: 'UploadService', methods: 4, blurb: 'Upload sessions' },
		{ name: 'MinecraftService', methods: 3, blurb: 'Versions, loaders' }
	];

	// Plain-REST endpoints that live outside the ConnectRPC services (and
	// therefore outside the generated OpenAPI spec). Paths verified against
	// internal/rpc/server.go and internal/rpc/handlers/.
	const restEndpoints: { method: string; path: string; blurb: string }[] = [
		{ method: 'GET', path: '/api/v1/openapi.yaml', blurb: 'Live OpenAPI spec backing this viewer' },
		{
			method: 'GET',
			path: '/api/v1/nodes/scan',
			blurb: 'Compatibility daemon discovery; generated control: NodeService.ScanNodes'
		},
		{ method: 'GET', path: '/api/v1/packwiz/packs', blurb: 'Modpack Studio: list packs' },
		{ method: 'POST', path: '/api/v1/packwiz/packs', blurb: 'Modpack Studio: create pack' },
		{ method: 'POST', path: '/api/v1/packwiz/packs/import', blurb: 'Modpack Studio: import pack' },
		{
			method: 'GET',
			path: '/api/v1/packwiz/packs/{id}',
			blurb: 'Modpack Studio: read / update / delete, clone, refresh'
		},
		{
			method: 'POST',
			path: '/api/v1/packwiz/packs/{id}/deploy',
			blurb: 'Modpack Studio: deploy pack to a server'
		},
		{
			method: 'POST',
			path: '/api/v1/packwiz/packs/{id}/migrate',
			blurb: 'Modpack Studio: migrate pack across loaders/versions'
		},
		{
			method: 'GET',
			path: '/api/v1/packwiz/loaders/{loader}/versions',
			blurb: 'Loader + game-version matrix'
		},
		{
			method: 'GET',
			path: '/api/v1/packwiz/{id}/{file}',
			blurb: 'pack.toml serving for container bootstrap (no auth)'
		},
		{
			method: 'GET',
			path: '/api/v1/servers/{id}/mods/search',
			blurb: 'Online mod search (Modrinth / CurseForge)'
		},
		{
			method: 'POST',
			path: '/api/v1/servers/{id}/mods/install',
			blurb: 'One-click mod install to a server'
		},
		{ method: 'GET', path: '/api/v1/upload/{session}', blurb: 'Streaming file upload session' },
		{ method: 'GET', path: '/api/v1/download/{session}', blurb: 'Streaming file download session' },
		{
			method: 'POST',
			path: '/api/v1/settings/validate-key',
			blurb:
				'Legacy adapter for API key / credential validation (use ConfigService.ValidateProviderKey)'
		},
		{ method: 'GET', path: '/api/v1/auth/oidc/callback', blurb: 'OIDC sign-in callback' }
	];

	function restTagType(method: string): 'green' | 'blue' | 'purple' | 'gray' {
		if (method === 'GET') return 'green';
		if (method === 'POST') return 'blue';
		if (method === 'PUT' || method === 'PATCH' || method === 'DELETE') return 'purple';
		return 'gray';
	}
</script>

<div class="w-full space-y-6 bg-background text-foreground">
	<CarbonBreadcrumbs items={[{ label: 'Docs' }, { label: 'API Reference' }]} />

	<!-- Carbon Page Header -->
	<CarbonPageHeader title="API Reference" description="Generated from the service OpenAPI spec.">
		{#snippet tag()}
			<CarbonTag type="blue" size="sm">ConnectRPC</CarbonTag>
			<CarbonTag type="gray" size="sm">15 services · 155 methods</CarbonTag>
		{/snippet}
		{#snippet actions()}
			<!-- eslint-disable svelte/no-navigation-without-resolve -- backend API endpoint, not a SvelteKit route; resolve() is inapplicable -->
			<a
				href="/api/v1/openapi.yaml"
				class="mono-label-accent inline-flex h-8 items-center px-3 hover:bg-accent hover:text-foreground"
			>
				Open raw spec
			</a>
			<!-- eslint-enable svelte/no-navigation-without-resolve -->
		{/snippet}
	</CarbonPageHeader>

	<!-- Carbon DataTable shell: interactive Scalar viewer -->
	<div class="flex flex-col border border-border bg-card">
		<div class="flex items-center justify-between border-b border-border bg-card p-4">
			<div>
				<h2 class="font-sans text-sm font-semibold tracking-wider text-foreground uppercase">
					Interactive Reference
				</h2>
				<p class="mt-0.5 font-sans text-xs text-muted-foreground">
					Scalar viewer · servers, modpacks, nodes, migration, Packwiz
				</p>
			</div>
			<CarbonTag type={isLoading ? 'gray' : 'green'} size="sm">
				{isLoading ? 'Loading' : 'Live'}
			</CarbonTag>
		</div>

		<!--
			Height fix: the previous `h-full` / `flex-1` chain resolved against
			auto-height ancestors (CarbonShell content wrapper), so the iframe
			collapsed to its ~150px default. A viewport-relative height with a
			minimum floor keeps the viewer filling available space without
			touching the shared shell layout.
		-->
		<div class="relative h-[calc(100vh-20rem)] min-h-[420px] w-full overflow-hidden bg-background">
			{#if isLoading}
				<div class="absolute inset-0 z-10 flex items-center justify-center bg-background">
					<div class="w-full max-w-md px-8">
						<div class="mb-4 text-center">
							<p class="mono-label">Loading API documentation…</p>
						</div>
						<div class="h-1.5 w-full overflow-hidden bg-secondary">
							<div
								class="h-full bg-primary transition-all duration-300"
								style="width: {loadingProgress}%"
							></div>
						</div>
					</div>
				</div>
			{/if}
			<iframe
				bind:this={iframeElement}
				id="openapispecs"
				title="API Documentation"
				class="absolute inset-0 h-full w-full border-0 {isLoading ? 'invisible' : ''}"
				style="background: var(--background);"
				referrerpolicy="same-origin"
				sandbox="allow-scripts allow-same-origin"
			></iframe>
		</div>
	</div>

	<!-- Carbon Structured List: ConnectRPC service catalog -->
	<div class="flex flex-col border border-border bg-card">
		<div class="border-b border-border bg-card p-4">
			<h2 class="font-sans text-sm font-semibold tracking-wider text-foreground uppercase">
				Service Catalog
			</h2>
			<p class="mt-0.5 font-sans text-xs text-muted-foreground">
				All ConnectRPC services in the live spec · procedure root
				<span class="font-mono text-muted-foreground"
					>/carbonpanel.v1.&lt;Service&gt;/&lt;Method&gt;</span
				>
			</p>
		</div>
		<div class="motion-stagger-fade grid gap-px bg-secondary sm:grid-cols-2 lg:grid-cols-3">
			{#each connectServices as service (service.name)}
				<div class="bg-card p-4 transition-colors hover:bg-accent">
					<div class="flex items-center justify-between gap-2">
						<span class="truncate font-mono text-sm text-foreground">{service.name}</span>
						<CarbonTag type="cyan" size="sm">{service.methods} methods</CarbonTag>
					</div>
					<p class="mt-1 truncate font-mono text-xs text-muted-foreground">
						/carbonpanel.v1.{service.name}/…
					</p>
					<p class="mt-1 text-xs text-muted-foreground">{service.blurb}</p>
				</div>
			{/each}
		</div>
	</div>

	<!-- Carbon DataTable: plain-REST endpoints outside the Connect spec -->
	<div class="flex flex-col border border-border bg-card">
		<div class="border-b border-border bg-card p-4">
			<h2 class="font-sans text-sm font-semibold tracking-wider text-foreground uppercase">
				Additional REST Endpoints
			</h2>
			<p class="mt-0.5 font-sans text-xs text-muted-foreground">
				Packwiz Studio, mod search, streaming transfers — served outside ConnectRPC, not in the
				generated spec
			</p>
		</div>
		<div class="overflow-x-auto">
			<table class="w-full border-collapse text-left font-sans text-sm">
				<thead class="border-b border-border bg-secondary text-foreground">
					<tr>
						<th scope="col" class="px-4 py-2.5 text-xs font-semibold tracking-wider uppercase"
							>Method</th
						>
						<th scope="col" class="px-4 py-2.5 text-xs font-semibold tracking-wider uppercase"
							>Path</th
						>
						<th scope="col" class="px-4 py-2.5 text-xs font-semibold tracking-wider uppercase"
							>Description</th
						>
					</tr>
				</thead>
				<tbody class="divide-y divide-border bg-card text-foreground">
					{#each restEndpoints as endpoint (endpoint.method + endpoint.path)}
						<tr class="transition-colors hover:bg-accent">
							<td class="px-4 py-3 align-middle">
								<CarbonTag type={restTagType(endpoint.method)} size="sm"
									>{endpoint.method}</CarbonTag
								>
							</td>
							<td class="px-4 py-3 align-middle font-mono text-xs text-muted-foreground"
								>{endpoint.path}</td
							>
							<td class="px-4 py-3 align-middle text-xs text-muted-foreground">{endpoint.blurb}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	</div>
</div>
