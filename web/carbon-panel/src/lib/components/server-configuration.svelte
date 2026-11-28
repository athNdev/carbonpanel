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
	import UpdateBatch from '$lib/components/fragments/UpdateBatch.svelte';
	import { onMount, tick, untrack } from 'svelte';
	import { SvelteMap, SvelteSet, SvelteURL } from 'svelte/reactivity';
	import { rpcClient } from '$lib/api/rpc-client';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Switch } from '$lib/components/ui/switch';

	import { toast } from 'svelte-sonner';
	import { Save, RefreshCw, Loader2, Link, Send, ChevronRight, Wand2 } from '@lucide/svelte';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { dirtySections } from '$lib/stores/dirty-sections.svelte';
	import { rebaselineConfigAfterSave } from '$lib/components/server-configuration-save';
	import { CarbonButton, CarbonSearch, CarbonSelect, CarbonSpinner } from '$lib/components/carbon';
	import ServerTabPanel from '$lib/components/server-ui/ServerTabPanel.svelte';
	import type { Server } from '$lib/proto/carbonpanel/v1/common_pb';
	import {
		ConfigApplyWindow,
		type ConfigCategory,
		type ConfigProperty
	} from '$lib/proto/carbonpanel/v1/config_pb';

	interface Props {
		server?: Server;
		config?: ConfigCategory[];
		onSave?: (updates: Record<string, string>) => Promise<void>;
		saving?: boolean;
	}

	let { server, config, onSave, saving: externalSaving = false }: Props = $props();
	const fieldControlPrefix = $props.id();

	// State
	let loading = $state(false);
	let saving = $state(false);
	let syncing = $state(false);
	let applyWindow = $state<'next' | 'time' | 'idle'>('next');
	let applyAtLocal = $state('');
	let idleMinutes = $state(15);
	let batchOpen = $state(false);
	let batchItems = $state<Array<{ id: string; kind: string; action: string; summary: string }>>([]);
	let categories = $state<ConfigCategory[]>([]);
	let activeCategory = $state('all');
	let search = $state('');
	let changesOnly = $state(false);
	let highlightedField = $state<string | null>(null);
	let expanded = new SvelteSet<string>();

	// Track original and current values
	let originalValues = $state<Map<string, string | null>>(new Map());
	let currentValues = $state<Map<string, string | null>>(new Map());
	let originalEnabled = $state<Set<string>>(new Set());
	let currentEnabled = $state<Set<string>>(new Set());

	// Derived state
	let isSaving = $derived(externalSaving || saving);

	// Get filtered categories (hide empty ones and filter system fields for global)
	let filteredCategories = $derived.by(() => {
		return categories
			.map((cat) => ({
				...cat,
				properties: !server ? cat.properties.filter((p) => !p.system) : cat.properties
			}))
			.filter((cat) => cat.properties.length > 0);
	});

	function parseCurseForgeUrl(url: string): { slug: string; fileId: string } {
		let slug = '';
		let fileId = '';
		if (!url) return { slug, fileId };
		const trimmed = url.trim();

		const fileMatch = trimmed.match(/\/files\/(\d+)/);
		if (fileMatch) {
			fileId = fileMatch[1];
		}

		const slugMatch = trimmed.match(
			/\/(?:minecraft\/(?:modpacks|mc-mods|customization|worlds|texture-packs)|projects)\/([a-zA-Z0-9_-]+)/
		);
		if (slugMatch) {
			slug = slugMatch[1];
		} else if (
			!trimmed.startsWith('http://') &&
			!trimmed.startsWith('https://') &&
			!trimmed.includes('/')
		) {
			slug = trimmed;
		}

		return { slug, fileId };
	}

	function handleExtractFromPageUrl(url: string) {
		const { slug, fileId } = parseCurseForgeUrl(url);
		const newValues = new SvelteMap(currentValues);
		const newEnabled = new SvelteSet(currentEnabled);
		let updated = false;

		if (slug) {
			newValues.set('cfSlug', slug);
			newEnabled.add('cfSlug');
			updated = true;
		}
		if (fileId) {
			newValues.set('cfFileId', fileId);
			newEnabled.add('cfFileId');
			updated = true;
		}

		if (updated) {
			currentValues = newValues;
			currentEnabled = newEnabled;
			toast.success(
				`Extracted from URL: ${slug ? `Slug: ${slug}` : ''}${slug && fileId ? ', ' : ''}${fileId ? `File ID: ${fileId}` : ''}`
			);
		} else {
			toast.info('Could not find slug or file ID in the entered URL');
		}
	}

	function assumeCfSlugFromServerName() {
		if (!server?.name) return;
		const slug = server.name
			.toLowerCase()
			.trim()
			.replace(/[^a-z0-9]+/g, '-')
			.replace(/^-|-$/g, '');
		if (slug) {
			const newValues = new SvelteMap(currentValues);
			newValues.set('cfSlug', slug);
			currentValues = newValues;
			const newEnabled = new SvelteSet(currentEnabled);
			newEnabled.add('cfSlug');
			currentEnabled = newEnabled;
			toast.success(`Assumed CurseForge slug from server name: "${slug}"`);
		}
	}

	// Calculate modified fields
	let modifiedFields = $derived.by(() => {
		const modified = new SvelteSet<string>();
		for (const [key, value] of currentValues) {
			const origValue = originalValues.get(key);
			const wasEnabled = originalEnabled.has(key);
			const isEnabled = currentEnabled.has(key);

			if (wasEnabled !== isEnabled) {
				modified.add(key);
				continue;
			}

			if (isEnabled && value !== origValue) {
				modified.add(key);
			}
		}
		return modified;
	});

	let visibleGroups = $derived.by(() => {
		const words = search.trim().toLowerCase().split(/\s+/).filter(Boolean);
		return filteredCategories
			.filter((cat) => activeCategory === 'all' || getCategoryId(cat.name) === activeCategory)
			.map((cat) => ({
				name: cat.name,
				properties: cat.properties.filter((p) => {
					if (changesOnly && !modifiedFields.has(p.key)) return false;
					const text = [cat.name, p.label, p.key, p.envVar, p.description].join(' ').toLowerCase();
					return words.every((word) => text.includes(word));
				})
			}))
			.filter((group) => group.properties.length);
	});

	let hasChanges = $derived(modifiedFields.size > 0);

	// Initialize URL hash handling
	onMount(() => {
		checkUrlHash();
		void loadBatch();
		window.addEventListener('hashchange', checkUrlHash);
		return () => window.removeEventListener('hashchange', checkUrlHash);
	});

	async function loadBatch() {
		if (!server) return;
		try {
			const batch = await rpcClient.config.getUpdateBatch({ serverId: server.id });
			batchOpen = batch.open;
			batchItems = batch.items ?? [];
			if (batch.window === ConfigApplyWindow.AT_TIME) applyWindow = 'time';
			else if (batch.window === ConfigApplyWindow.IDLE) applyWindow = 'idle';
			else if (batch.open) applyWindow = 'next';
			if (batch.idleMinutes > 0) idleMinutes = batch.idleMinutes;
			if (batch.applyAtUnix) {
				const when = new Date(Number(batch.applyAtUnix) * 1000);
				const pad = (n: number) => String(n).padStart(2, '0');
				applyAtLocal = `${when.getFullYear()}-${pad(when.getMonth() + 1)}-${pad(when.getDate())}T${pad(when.getHours())}:${pad(when.getMinutes())}`;
			}
		} catch (error) {
			console.error(error);
		}
	}

	async function discardStaged(itemId: string) {
		if (!server) return;
		try {
			await rpcClient.config.discardUpdateBatch({ serverId: server.id, itemId });
			await loadBatch();
			toast.success(itemId ? 'Staged item discarded' : 'Staged batch discarded');
		} catch (error) {
			toast.error('Failed to discard the staged update');
			console.error(error);
		}
	}

	// React to config prop changes (for global settings)
	let previousConfig = $state<ConfigCategory[] | undefined>(undefined);
	$effect(() => {
		if (config && config !== previousConfig) {
			untrack(() => {
				previousConfig = config;
				processConfig(config);
			});
		}
	});

	// Reload when server changes
	let previousServerId = $state<string | undefined>(undefined);
	$effect(() => {
		if (server && server.id !== previousServerId) {
			untrack(() => {
				previousServerId = server!.id;
				expanded.clear();
				search = '';
				changesOnly = false;
				activeCategory = 'all';
				loadServerConfig();
				void loadBatch();
			});
		}
	});

	async function loadServerConfig() {
		if (!server) return;
		const requestedId = server.id;
		loading = true;
		try {
			const response = await rpcClient.config.getServerConfig({ serverId: requestedId });
			if (server?.id !== requestedId) return;
			processConfig(response.categories);
		} catch (error) {
			if (server?.id !== requestedId) return;
			toast.error('Failed to load server configuration');
			console.error(error);
		} finally {
			if (server?.id === requestedId) loading = false;
		}
	}

	function processConfig(configData: ConfigCategory[]) {
		categories = configData;

		const newOriginalValues = new SvelteMap<string, string | null>();
		const newCurrentValues = new SvelteMap<string, string | null>();
		const newOriginalEnabled = new SvelteSet<string>();
		const newCurrentEnabled = new SvelteSet<string>();

		const isGlobal = !server;

		for (const category of configData) {
			for (const prop of category.properties) {
				if (['id', 'serverId', 'updatedAt'].includes(prop.key)) continue;

				const value = prop.value || null;
				newOriginalValues.set(prop.key, value);
				newCurrentValues.set(prop.key, value);

				const hasValue = value !== null && value !== '';
				const shouldEnable = isGlobal
					? hasValue || prop.required
					: hasValue || prop.required || prop.system;

				if (shouldEnable) {
					newOriginalEnabled.add(prop.key);
					newCurrentEnabled.add(prop.key);
				}
			}
		}

		originalValues = newOriginalValues;
		currentValues = newCurrentValues;
		originalEnabled = newOriginalEnabled;
		currentEnabled = newCurrentEnabled;
		void tick().then(checkUrlHash);
	}

	/** Error whose message is already user-facing; the callers toast it verbatim. */
	class ConfigurationSaveError extends Error {}

	/**
	 * OSS-464: after a successful save, move the baseline to exactly what was
	 * sent and restore any entry the user edited while the request was in
	 * flight as a dirty edit against that baseline — nothing typed during a
	 * save is silently dropped by the response refresh (same convention as
	 * Server Settings).
	 */
	function rebaselineAfterSave(
		sent: Record<string, string>,
		inFlightValues: ReadonlyMap<string, string | null>,
		inFlightEnabled: ReadonlySet<string>
	): void {
		const next = rebaselineConfigAfterSave(
			{ originalValues, originalEnabled, currentValues, currentEnabled },
			sent,
			inFlightValues,
			inFlightEnabled
		);
		originalValues = new SvelteMap(next.originalValues);
		originalEnabled = new SvelteSet(next.originalEnabled);
		currentValues = new SvelteMap(next.currentValues);
		currentEnabled = new SvelteSet(next.currentEnabled);
	}

	/**
	 * Shared save core: validates, persists and refreshes. Throws on failure —
	 * the callers own the toasts so the section buttons and the floating bar
	 * keep identical feedback.
	 */
	async function stageChanges(stage: boolean): Promise<void> {
		if (!hasChanges) throw new ConfigurationSaveError('No changes to save');
		const stageUpdate = !!server && stage;
		if (stageUpdate && applyWindow === 'time' && !applyAtLocal) {
			throw new ConfigurationSaveError('Choose when the batch should apply');
		}
		if (stageUpdate && applyWindow === 'idle' && idleMinutes < 1) {
			throw new ConfigurationSaveError('Idle minutes must be at least 1');
		}
		const windowValue =
			applyWindow === 'time'
				? ConfigApplyWindow.AT_TIME
				: applyWindow === 'idle'
					? ConfigApplyWindow.IDLE
					: ConfigApplyWindow.NEXT_RESTART;
		const updates: Record<string, string> = {};

		for (const key of modifiedFields) {
			if (currentEnabled.has(key)) {
				const value = currentValues.get(key);
				updates[key] = value ?? '';
			} else {
				updates[key] = '';
			}
		}

		if (onSave) {
			await onSave(updates);
		} else if (server) {
			const sent = { ...updates };
			const response = await rpcClient.config.updateServerConfig({
				serverId: server.id,
				updates,
				stage: stageUpdate,
				window: stageUpdate ? windowValue : ConfigApplyWindow.UNSPECIFIED,
				applyAtUnix:
					stageUpdate && applyWindow === 'time'
						? BigInt(Math.floor(new Date(applyAtLocal).getTime() / 1000))
						: 0n,
				idleMinutes: stageUpdate && applyWindow === 'idle' ? idleMinutes : 0
			});
			// Capture the form as the user sees it only after the request
			// resolves: anything typed while it was in flight is still live
			// here, and the response refresh must not drop it.
			const inFlightValues = new SvelteMap(currentValues);
			const inFlightEnabled = new SvelteSet(currentEnabled);
			processConfig(response.categories);
			rebaselineAfterSave(sent, inFlightValues, inFlightEnabled);
			await loadBatch();
		}
	}

	function toastStageFailure(stage: boolean, error: unknown): string {
		const stageUpdate = !!server && stage;
		if (error instanceof ConfigurationSaveError) {
			toast.error(error.message);
		} else {
			toast.error(stageUpdate ? 'Failed to stage configuration' : 'Failed to save configuration');
			console.error(error);
		}
		return error instanceof Error ? error.message : 'Unknown error';
	}

	/**
	 * One save path for the section buttons and the floating action bar
	 * (OSS-464): identical toasts; `rethrow` lets the bar aggregate failures
	 * while the buttons keep swallowing them. On failure the loaded snapshot is
	 * untouched, so the section stays dirty.
	 */
	async function runStage(stage: boolean, rethrow: boolean): Promise<void> {
		const stageUpdate = !!server && stage;
		saving = true;
		try {
			await stageChanges(stage);
			toast.success(stageUpdate ? 'Configuration staged' : 'Configuration saved');
			if (stageUpdate) {
				const when =
					applyWindow === 'time'
						? 'at the chosen time'
						: applyWindow === 'idle'
							? `after ${idleMinutes} idle minutes`
							: 'on the next restart';
				toast.info(`The server was not restarted. These changes apply ${when}.`);
			}
		} catch (error) {
			const reason = toastStageFailure(stage, error);
			if (rethrow) throw error instanceof Error ? error : new Error(reason);
		} finally {
			saving = false;
		}
	}

	async function handleSave(stage = true) {
		if (!hasChanges) {
			toast.info('No changes to save');
			return;
		}
		await runStage(stage, false);
	}

	async function handleSyncToAllServers(opsAndWhitelistOnly: boolean) {
		if (hasChanges) {
			toast.error('Please save your pending changes first before syncing to servers');
			return;
		}
		syncing = true;
		try {
			const res = await rpcClient.config.syncGlobalSettingsToServers({
				opsAndWhitelistOnly
			});
			toast.success(res.message || 'Settings synced to all servers');
		} catch (error) {
			toast.error('Failed to sync settings to servers');
			console.error(error);
		} finally {
			syncing = false;
		}
	}

	function handleReset() {
		currentValues = new SvelteMap(originalValues);
		currentEnabled = new SvelteSet(originalEnabled);
	}

	// OSS-464: registered only for a server's configuration — the global
	// defaults page passes no server and is not part of the server-view bar.
	// Reset maps to the bar's undo affordance (revert to the loaded snapshot,
	// client-side only), and the bar's save keeps the staged (Stage button)
	// semantics. Closures keep the reactivity: the bar's $derived tracks
	// hasChanges/isSaving through them.
	onMount(() => {
		if (!server) return;
		dirtySections.register({
			id: 'configuration',
			label: 'Server Configuration',
			isDirty: () => hasChanges,
			save: () => runStage(true, true),
			undo: handleReset,
			saving: () => isSaving
		});
		return () => dirtySections.unregister('configuration');
	});

	function toggleFieldEnabled(key: string, enabled: boolean, prop: ConfigProperty) {
		const newEnabled = new SvelteSet(currentEnabled);
		const newValues = new SvelteMap(currentValues);

		if (enabled) {
			newEnabled.add(key);
			if (!newValues.get(key)) {
				newValues.set(key, prop.defaultValue ?? getDefaultForType(prop.type));
			}
		} else {
			newEnabled.delete(key);
		}

		currentEnabled = newEnabled;
		currentValues = newValues;
	}

	function updateValue(key: string, value: string | boolean) {
		const newValues = new SvelteMap(currentValues);
		const strValue = typeof value === 'boolean' ? String(value) : value;
		newValues.set(key, strValue || null);
		currentValues = newValues;

		// Auto-extract slug and file ID when user enters/pastes a CurseForge Page URL
		if (key === 'cfPageUrl' && typeof strValue === 'string' && strValue.trim().length > 10) {
			const { slug, fileId } = parseCurseForgeUrl(strValue);
			let changed = false;
			if (slug && !currentValues.get('cfSlug')) {
				newValues.set('cfSlug', slug);
				const newEnabled = new SvelteSet(currentEnabled);
				newEnabled.add('cfSlug');
				currentEnabled = newEnabled;
				changed = true;
			}
			if (fileId && !currentValues.get('cfFileId')) {
				newValues.set('cfFileId', fileId);
				const newEnabled = new SvelteSet(currentEnabled);
				newEnabled.add('cfFileId');
				currentEnabled = newEnabled;
				changed = true;
			}
			if (changed) {
				currentValues = newValues;
			}
		}
	}

	function getDefaultForType(type: string): string {
		switch (type) {
			case 'number':
				return '0';
			case 'checkbox':
				return 'false';
			default:
				return '';
		}
	}

	function getDisplayValue(prop: ConfigProperty): string {
		const value = currentValues.get(prop.key);
		const isEnabled = currentEnabled.has(prop.key);

		if (isEnabled && value !== null && value !== undefined) {
			return value;
		}
		return prop.defaultValue ?? '';
	}

	function getBooleanValue(prop: ConfigProperty): boolean {
		const value = getDisplayValue(prop);
		return value.toLowerCase() === 'true';
	}

	function getCategoryId(name: string): string {
		return name.toLowerCase().replace(/\s+/g, '-');
	}

	function selectCategory(categoryId: string) {
		activeCategory = categoryId;
		highlightedField = null;
		const url = new SvelteURL(window.location.href);
		url.hash = categoryId === 'all' ? '' : categoryId;
		window.history.replaceState(
			window.history.state,
			'',
			`${url.pathname}${url.search}${url.hash}`
		);
	}

	async function copyLinkToClipboard(anchor: string) {
		const url = new SvelteURL(window.location.href);
		url.searchParams.set('tab', server ? 'configuration' : 'server-config');
		url.hash = anchor;
		const success = await copyToClipboard(url.toString());
		if (success) {
			toast.success('Link copied to clipboard');
		}
	}

	async function checkUrlHash() {
		const hash = window.location.hash.slice(1);
		if (!hash) return;
		const category =
			filteredCategories.find((cat) => getCategoryId(cat.name) === hash) ??
			filteredCategories.find((cat) => cat.properties.some((p) => p.key === hash));
		if (!category) return;
		search = '';
		changesOnly = false;
		activeCategory = getCategoryId(category.name);
		highlightedField = hash === activeCategory ? null : hash;
		if (highlightedField) {
			await tick();
			if (window.location.hash.slice(1) === hash)
				document.getElementById(hash)?.scrollIntoView({ block: 'center' });
		}
	}

	function canToggleField(prop: ConfigProperty): boolean {
		if (prop.required) return false;
		if (prop.system) return false;
		return true;
	}
</script>

<ServerTabPanel
	mode="form"
	title={!server ? 'Default Server Configuration' : 'Server Configuration'}
	description={!server
		? 'Configure default values for new servers'
		: 'Configure Minecraft server environment variables'}
>
	{#snippet chip()}
		{#if hasChanges}
			<span class="font-mono text-xs whitespace-nowrap text-warning-text">
				{modifiedFields.size} unsaved {modifiedFields.size === 1 ? 'change' : 'changes'}
			</span>
		{/if}
	{/snippet}
	{#snippet actions()}
		{#if !server}
			<CarbonButton
				kind="secondary"
				size="sm"
				class="gap-1.5"
				onclick={() => handleSyncToAllServers(true)}
				disabled={loading || syncing || hasChanges}
				title="Propagate configured default Ops and Whitelist to all existing servers"
			>
				{#if syncing}
					<Loader2 class="h-3.5 w-3.5 animate-spin" />
				{:else}
					<Send class="h-3.5 w-3.5" />
				{/if}
				<span>Sync Ops & Whitelist</span>
			</CarbonButton>
		{/if}
		<CarbonButton
			kind="secondary"
			size="sm"
			class="gap-1.5"
			onclick={handleReset}
			disabled={loading || !hasChanges}
		>
			<RefreshCw class="h-3.5 w-3.5" />
			<span>Reset</span>
		</CarbonButton>
		{#if server}
			<CarbonButton
				kind="secondary"
				size="sm"
				onclick={() => handleSave(false)}
				disabled={loading || isSaving || !hasChanges}
				title="Save these values and restart the container"
			>
				<span>Apply now</span>
			</CarbonButton>
		{/if}
		<CarbonButton
			kind="primary"
			size="sm"
			class="gap-1.5"
			onclick={() => handleSave(true)}
			disabled={loading || isSaving || !hasChanges}
		>
			{#if isSaving}
				<Loader2 class="h-3.5 w-3.5 animate-spin" />
			{:else}
				<Save class="h-3.5 w-3.5" />
			{/if}
			<span>{server ? 'Stage' : 'Save'}</span>
		</CarbonButton>
	{/snippet}
	{#snippet toolbar()}
		{#if filteredCategories.length}
			<div class="config-toolbar">
				<div class="config-search">
					<label for={`${fieldControlPrefix}-search`}>Search settings</label>
					<CarbonSearch
						id={`${fieldControlPrefix}-search`}
						type="search"
						aria-label="Search settings"
						placeholder="Name, environment variable or description"
						bind:value={search}
					/>
				</div>
				<div class="config-filters">
					<CarbonSelect
						class="min-w-0"
						label="Configuration category"
						value={activeCategory}
						onchange={(event) => selectCategory(event.currentTarget.value)}
					>
						<option value="all">All categories</option>
						{#each filteredCategories as category (category.name)}
							<option value={getCategoryId(category.name)}>{category.name}</option>
						{/each}
					</CarbonSelect>
					<button
						type="button"
						class="change-filter focus-ring"
						aria-pressed={changesOnly}
						onclick={() => (changesOnly = !changesOnly)}
					>
						Unsaved ({modifiedFields.size})
					</button>
				</div>
			</div>
		{/if}
	{/snippet}
	<div class="config-list">
		{#if loading}
			<div class="flex items-center justify-center py-8">
				<CarbonSpinner size="md" label="Loading" description="Loading configuration..." />
			</div>
		{:else if !filteredCategories.length}
			<p class="p-4 text-sm text-muted-foreground">No configuration is available.</p>
		{:else}
			{#if !visibleGroups.length}
				<div class="flex flex-wrap items-center gap-3 p-4" role="status">
					<p>No matching settings</p>
					<CarbonButton
						kind="tertiary"
						size="sm"
						onclick={() => {
							search = '';
							changesOnly = false;
							selectCategory('all');
						}}>Clear filters</CarbonButton
					>
				</div>
			{/if}
			{#each visibleGroups as group (group.name)}
				<section aria-label={group.name} class="config-group">
					<h3>{group.name} <span>{group.properties.length}</span></h3>
					{#each group.properties as prop (prop.key)}
						{@const isEnabled = currentEnabled.has(prop.key)}
						{@const canToggle = canToggleField(prop)}
						{@const isModified = modifiedFields.has(prop.key)}
						{@const controlId = `${fieldControlPrefix}-${prop.key}`}
						<div
							id={prop.key}
							data-field="true"
							class="config-row"
							class:modified={isModified}
							class:highlighted={highlightedField === prop.key}
						>
							<div class="setting-name">
								<Label id={`${controlId}-label`} for={controlId}>{prop.label || prop.key}</Label>
								{#if prop.envVar}<code>{prop.envVar}</code>{/if}
							</div>
							<div class="min-w-0">
								{#if prop.type === 'checkbox'}
									<div class="flex items-center gap-2">
										<Switch
											id={controlId}
											aria-labelledby={`${controlId}-label`}
											checked={getBooleanValue(prop)}
											onCheckedChange={(checked) => updateValue(prop.key, checked)}
											disabled={prop.system || !isEnabled}
										/>
										<span class="text-xs">{getBooleanValue(prop) ? 'On' : 'Off'}</span>
									</div>
								{:else if prop.type === 'select' && prop.options?.length}
									<select
										id={controlId}
										class="h-9 w-full px-3 text-sm"
										value={getDisplayValue(prop)}
										disabled={prop.system || !isEnabled}
										onchange={(event) => updateValue(prop.key, event.currentTarget.value)}
									>
										{#if !prop.options.includes(getDisplayValue(prop))}
											<option value={getDisplayValue(prop)}
												>{getDisplayValue(prop) || 'Select…'}</option
											>
										{/if}
										{#each prop.options as option (option)}<option value={option}
												>{option || '(empty)'}</option
											>{/each}
									</select>
								{:else}
									<Input
										id={controlId}
										type={prop.type === 'number' || prop.type === 'password' ? prop.type : 'text'}
										value={getDisplayValue(prop)}
										placeholder={prop.type === 'password' ? '' : (prop.defaultValue ?? '')}
										oninput={(event) => updateValue(prop.key, event.currentTarget.value)}
										disabled={prop.system || !isEnabled}
										class="h-9"
									/>
								{/if}
							</div>
							<div class="field-actions">
								{#if canToggle}
									<button
										type="button"
										class="source-button focus-ring"
										aria-pressed={isEnabled}
										aria-label={`Override ${prop.label || prop.key}`}
										title={isEnabled ? 'Use the default value' : 'Set a custom value'}
										onclick={() => toggleFieldEnabled(prop.key, !isEnabled, prop)}
									>
										{isEnabled ? 'Override' : 'Default'}
									</button>
								{:else}
									<span class="text-xs text-muted-foreground"
										>{prop.system ? 'Managed' : 'Required'}</span
									>
								{/if}
							</div>
							<button
								type="button"
								class="setting-help focus-ring"
								aria-label={`Details for ${prop.label || prop.key}`}
								aria-expanded={expanded.has(prop.key)}
								aria-controls={`${controlId}-help`}
								onclick={() =>
									expanded.has(prop.key) ? expanded.delete(prop.key) : expanded.add(prop.key)}
							>
								<ChevronRight size={14} aria-hidden="true" /><span>Details</span>
							</button>
							<div
								id={`${controlId}-help`}
								hidden={!expanded.has(prop.key)}
								class="setting-description"
							>
								{#if prop.description}<p>{prop.description}</p>{/if}
								{#if prop.defaultValue && prop.type !== 'password'}<p>
										Default: <code>{prop.defaultValue}</code>
									</p>{/if}
								{#if prop.key === 'cfApiKey'}<p>
										Optional. Keyless downloads are available through community proxies.
									</p>{/if}
								<div class="flex flex-wrap gap-2">
									<CarbonButton
										kind="tertiary"
										size="sm"
										class="gap-1.5"
										onclick={() => copyLinkToClipboard(prop.key)}
									>
										<Link size={14} /> Copy link
									</CarbonButton>
									{#if prop.key === 'cfSlug' && server?.name}
										<CarbonButton kind="tertiary" size="sm" onclick={assumeCfSlugFromServerName}>
											<Wand2 size={14} /> Use server name
										</CarbonButton>
									{:else if prop.key === 'cfPageUrl' && getDisplayValue(prop)}
										<CarbonButton
											kind="tertiary"
											size="sm"
											onclick={() => handleExtractFromPageUrl(getDisplayValue(prop))}
										>
											<Wand2 size={14} /> Extract slug and file ID
										</CarbonButton>
									{/if}
								</div>
							</div>
						</div>
					{/each}
				</section>
			{/each}

			{#if server}
				<UpdateBatch
					bind:applyWindow
					bind:applyAtLocal
					bind:idleMinutes
					{batchOpen}
					{batchItems}
					{discardStaged}
				/>
			{/if}
		{/if}
	</div>
</ServerTabPanel>

<style>
	.config-toolbar {
		display: flex;
		flex-wrap: wrap;
		gap: var(--carbon-space-3);
		padding: var(--carbon-space-4);
		background: var(--card);
	}
	.config-search {
		flex: 2 1 17rem;
		min-width: 0;
		align-self: end;
		display: grid;
		gap: var(--carbon-space-1);
		font-size: 0.8125rem;
	}
	.config-search :global(input::-webkit-search-cancel-button) {
		appearance: none;
	}
	.config-filters {
		flex: 1 1 16rem;
		min-width: 0;
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: end;
		gap: var(--carbon-space-3);
	}
	.change-filter,
	.source-button {
		padding: 0.3rem 0.6rem;
		font-size: 0.8125rem;
		color: var(--muted-foreground);
		border: 1px solid var(--border);
		cursor: pointer;
	}
	.change-filter {
		min-height: var(--carbon-control-md);
		padding-inline: var(--carbon-space-3);
		white-space: nowrap;
		background: var(--field-surface);
	}
	.change-filter:hover,
	.source-button:hover {
		color: var(--foreground);
		border-color: var(--field-hover-edge);
	}
	.change-filter[aria-pressed='true'],
	.source-button[aria-pressed='true'] {
		color: var(--accent-text);
		background: var(--accent);
	}
	.config-list {
		container-type: inline-size;
	}
	.config-group > h3 {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
		padding: 0.5rem 1rem;
		background: var(--card);
		border-block-end: 1px solid var(--border);
		font-size: 0.875rem;
		font-weight: 600;
	}
	.config-group > h3 span,
	.setting-name code {
		color: var(--muted-foreground);
		font-size: 0.75rem;
		font-weight: 400;
	}
	.config-row {
		display: grid;
		grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr) 5rem 4.5rem;
		gap: 0.4rem 0.85rem;
		align-items: center;
		padding: 0.5rem 1rem;
		border-block-end: 1px solid var(--border);
	}
	.setting-name {
		display: grid;
		gap: 0.15rem;
		overflow-wrap: anywhere;
	}
	.field-actions {
		justify-self: end;
	}
	.config-row.modified {
		box-shadow: inset 3px 0 var(--warning);
	}
	.config-row.highlighted {
		outline: 2px solid var(--ring);
		outline-offset: -2px;
	}
	.setting-help {
		justify-self: end;
		display: inline-flex;
		align-items: center;
		gap: 0.2rem;
		cursor: pointer;
		color: var(--muted-foreground);
		min-height: 2rem;
		font-size: 0.8125rem;
	}
	.setting-help[aria-expanded='true'] :global(svg) {
		transform: rotate(90deg);
	}
	.setting-description {
		grid-column: 1 / -1;
		padding: 0.5rem 0;
		color: var(--muted-foreground);
		font-size: 0.875rem;
		overflow-wrap: anywhere;
	}
	.setting-description > * + * {
		margin-block-start: 0.4rem;
	}
	@container (max-width: 42rem) {
		.config-row {
			grid-template-columns: minmax(0, 1fr) auto;
		}
		.setting-name {
			grid-column: 1;
		}
		.config-row > :nth-child(2) {
			grid-column: 1;
			grid-row: 2;
		}
		.field-actions {
			grid-row: 2;
			grid-column: 2;
		}
		.setting-help {
			grid-row: 1;
			grid-column: 2;
		}
	}
</style>
