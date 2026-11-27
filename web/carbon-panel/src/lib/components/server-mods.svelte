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
	import { onDestroy } from 'svelte';
	import {
		Loader2,
		Upload,
		Download,
		Trash2,
		Package,
		FileText,
		X,
		Boxes,
		Search,
		Check,
		Ban,
		RefreshCw,
		CalendarClock
	} from '@lucide/svelte';
	import { rpcClient, wasRpcErrorToasted } from '$lib/api/rpc-client';
	import { toast } from 'svelte-sonner';
	import { ModLoader, type Server } from '$lib/proto/carbonpanel/v1/common_pb';
	import type { Mod } from '$lib/proto/carbonpanel/v1/mod_pb';
	import { ConfigApplyWindow } from '$lib/proto/carbonpanel/v1/config_pb';
	import {
		ExecutionStatus,
		ScheduleType,
		TaskStatus,
		TaskType,
		type ScheduledTask,
		type TaskExecution
	} from '$lib/proto/carbonpanel/v1/task_pb';
	import { formatBytes } from '$lib/utils';
	import {
		buildPolicyConfig,
		buildUpdateSelections,
		currentUpdateSelectionIds,
		DEFAULT_POLICY_STATE,
		isCurrentServer,
		policyFromPersistedTask,
		pruneUpdateSelections,
		updateCandidateFingerprint,
		updateStateClass
	} from '$lib/mod-update-ui';
	import { summarizeModUpdateCheck } from '$lib/utils/mod-update-summary';
	import { uploadFile, cancelUpload, type UploadProgress } from '$lib/utils/chunked-upload';
	import ModBrowserDialog from '$lib/components/mod-browser-dialog.svelte';
	import {
		CarbonTag,
		CarbonInlineLoading,
		CarbonButton,
		CarbonConfirm,
		CarbonModal
	} from '$lib/components/carbon';
	import ServerTabPanel from '$lib/components/server-ui/ServerTabPanel.svelte';

	interface Props {
		server: Server;
		active?: boolean;
	}

	let { server, active = false }: Props = $props();

	let mods = $state<Mod[]>([]);
	let loading = $state(true);
	let uploading = $state(false);
	let uploadProgress = $state<UploadProgress | null>(null);
	let currentUploadFilename = $state('');
	let uploadAbortController = $state<AbortController | null>(null);
	let fileInput = $state<HTMLInputElement | null>(null);
	let browserDialogOpen = $state(false);

	let searchQuery = $state('');
	let modFilter = $state<'all' | 'enabled' | 'disabled'>('all');

	let hasLoaded = false;
	let requestNumber = 0;
	let updateStatus = $state('');
	// A check runs against an already-listed set. Keeping it separate from
	// `loading` leaves the rows on screen while the provider is queried.
	let checking = $state(false);
	let previousServerId = '';

	// OSS-424 manual review and apply. Selection is explicit: a preview never
	// stages bytes on its own, and only state=available rows can be selected.
	let selectedUpdates = $state<string[]>([]);
	// The reviewed candidate identity for each checked row. A selection is only
	// stageable while the current preview still matches this fingerprint.
	let selectedUpdateFingerprints = $state<Record<string, string>>({});
	// True only while `mods` came from a check that attached previews. A plain
	// reload returns no previews, so their absence must not invalidate a review.
	let previewsSupplied = $state(false);
	// Why a selection was dropped, kept apart from the check summary so a later
	// status update cannot hide it.
	let selectionNotice = $state('');
	let updateWindow = $state<'next' | 'time' | 'idle'>('next');
	let applyAtLocal = $state('');
	let idleMinutes = $state(15);
	let stagingUpdates = $state(false);

	// OSS-425 per-server opt-in policy. The policy is a typed mod_update task;
	// TaskService owns schedule, timezone, next/last run and bounded retries.
	let policyTask = $state<ScheduledTask | null>(null);
	let policyEnabled = $state(false);
	let policyCron = $state('0 3 * * *');
	let policyTimezone = $state('UTC');
	let policyExcluded = $state('');
	let policyMax = $state(5);
	let policySkipRunning = $state(true);
	let policyWindow = $state<'next_restart' | 'idle'>('next_restart');
	let policyIdleMinutes = $state(15);
	let policyLastOutcome = $state('No runs yet');
	let policySaving = $state(false);
	let policyLoading = $state(false);
	// The policy form is 555 px tall on a phone. Inline it consumed the whole
	// mods pane and left the list at 0 px, so it opens in a dialog instead.
	let policyOpen = $state(false);
	let policyLoadedServer = $state<string | null>(null);
	let policyReady = $derived(policyLoadedServer === server.id);
	let loadedPolicyOptIn = false;
	let policyGeneration = 0;
	onDestroy(() => {
		policyGeneration++;
	});

	let availableUpdates = $derived(mods.filter((mod) => mod.updatePreview?.state === 'available'));

	let currentSelectedUpdates = $derived(
		currentUpdateSelectionIds(mods, selectedUpdates, selectedUpdateFingerprints)
	);

	function toggleUpdateSelection(mod: Mod) {
		if (mod.updatePreview?.state !== 'available') return;
		selectionNotice = '';
		if (selectedUpdates.includes(mod.id)) {
			selectedUpdates = selectedUpdates.filter((selected) => selected !== mod.id);
			const fingerprints = { ...selectedUpdateFingerprints };
			delete fingerprints[mod.id];
			selectedUpdateFingerprints = fingerprints;
		} else {
			selectedUpdates = [...selectedUpdates, mod.id];
			selectedUpdateFingerprints = {
				...selectedUpdateFingerprints,
				[mod.id]: updateCandidateFingerprint(mod.updatePreview)
			};
		}
	}

	function applyWindowEnum(): ConfigApplyWindow {
		if (updateWindow === 'time') return ConfigApplyWindow.AT_TIME;
		if (updateWindow === 'idle') return ConfigApplyWindow.IDLE;
		return ConfigApplyWindow.NEXT_RESTART;
	}

	function timestampLabel(value: { seconds: bigint | number } | undefined): string {
		if (!value || !value.seconds) return '—';
		return new Date(Number(value.seconds) * 1000).toLocaleString();
	}

	let policyNextRun = $derived(
		timestampLabel(policyTask?.status === TaskStatus.ENABLED ? policyTask.nextRun : undefined)
	);
	let policyScheduleStatus = $derived(
		policyTask?.status === TaskStatus.ENABLED
			? 'Enabled'
			: policyTask?.status === TaskStatus.PAUSED
				? 'Paused'
				: policyTask?.status === TaskStatus.DISABLED
					? 'Disabled'
					: 'Not scheduled'
	);
	let policyLastRun = $derived(timestampLabel(policyTask?.lastRun));

	async function loadPolicy() {
		if (!canHaveMods() || policyLoading) return;
		const serverId = server.id;
		const generation = policyGeneration;
		const current = () => generation === policyGeneration && isCurrentServer(serverId, server.id);
		try {
			policyLoading = true;
			const response = await rpcClient.task.listTasks({ serverId });
			if (!current()) return;
			const task = response.tasks.find((candidate) => candidate.taskType === TaskType.MOD_UPDATE);
			policyTask = task ?? null;
			const state = policyFromPersistedTask(task ?? null, TaskStatus.ENABLED);
			policyEnabled = state.enabled;
			loadedPolicyOptIn = state.enabled;
			policyLoadedServer = serverId;
			policyCron = state.cron;
			policyTimezone = state.timezone;
			policyExcluded = state.excludedProjects;
			policyMax = state.maxPerRun;
			policySkipRunning = state.skipWhenRunning;
			policyWindow = state.window;
			policyIdleMinutes = state.idleMinutes;
			if (task) {
				const history = await rpcClient.task.listTaskExecutions({ taskId: task.id });
				if (!current()) return;
				const latest: TaskExecution | undefined = history.executions[0];
				if (latest) {
					const outcome =
						latest.status === ExecutionStatus.FAILED
							? `Failed: ${latest.error || 'unknown error'}`
							: latest.status === ExecutionStatus.TIMEOUT
								? 'Timed out'
								: latest.status === ExecutionStatus.COMPLETED
									? latest.output || 'Completed'
									: 'Last run recorded';
					policyLastOutcome = `${outcome} (${timestampLabel(latest.startedAt)})`;
				} else {
					policyLastOutcome = 'No runs yet';
				}
			} else {
				policyLastOutcome = 'No runs yet';
			}
		} catch (error) {
			if (current() && !wasRpcErrorToasted(error))
				toast.error('Failed to load the mod update policy');
		} finally {
			if (current()) policyLoading = false;
		}
	}

	async function savePolicy() {
		if (!policyReady || policyLoading || policySaving) return;
		const serverId = server.id;
		const generation = policyGeneration;
		const current = () => generation === policyGeneration && isCurrentServer(serverId, server.id);
		const task = policyTask;
		const enabled = policyEnabled;
		const explicitOptInChange = enabled !== loadedPolicyOptIn;
		const config = buildPolicyConfig({
			enabled,
			excludedProjects: policyExcluded,
			maxPerRun: policyMax,
			skipWhenRunning: policySkipRunning,
			window: policyWindow,
			idleMinutes: policyIdleMinutes
		});
		try {
			policySaving = true;
			let savedTask: ScheduledTask | null;
			if (task) {
				const updated = await rpcClient.task.updateTask({
					id: task.id,
					cronExpr: policyCron,
					timezone: policyTimezone,
					config
				});
				savedTask = updated.task ?? task;
				// Finish the mutation on the captured task even after navigation.
				if (explicitOptInChange) {
					const status = enabled ? TaskStatus.ENABLED : TaskStatus.DISABLED;
					if (savedTask.status !== status) {
						const toggled = await rpcClient.task.toggleTask({ id: task.id, status });
						savedTask = toggled.task ?? savedTask;
					}
				}
			} else {
				const created = await rpcClient.task.createTask({
					serverId,
					name: 'Automatic mod updates',
					description: 'Stages compatible Modrinth updates into the pending batch.',
					taskType: TaskType.MOD_UPDATE,
					schedule: ScheduleType.CRON,
					cronExpr: policyCron,
					timezone: policyTimezone,
					config,
					retryCount: 1,
					retryDelay: 60
				});
				savedTask = created.task ?? null;
			}
			if (!current()) return;
			policyTask = savedTask;
			loadedPolicyOptIn = enabled;
			toast.success('Mod update policy saved');
			await loadPolicy();
		} catch (error) {
			if (current() && !wasRpcErrorToasted(error))
				toast.error('Failed to save the mod update policy');
		} finally {
			if (current()) policySaving = false;
		}
	}

	$effect(() => {
		if (server.id !== previousServerId) {
			previousServerId = server.id;
			updateStatus = '';
			selectionNotice = '';
			selectedUpdates = [];
			selectedUpdateFingerprints = {};
			checking = false;
			mods = [];
			previewsSupplied = false;
			loading = true;
			uploading = false;
			hasLoaded = false;
			// Invalidate outstanding policy work even if navigation returns to
			// the same server before an earlier response arrives.
			policyGeneration++;
			policyTask = null;
			policyLoadedServer = null;
			policyLoading = false;
			loadedPolicyOptIn = false;
			policyEnabled = DEFAULT_POLICY_STATE.enabled;
			policyCron = DEFAULT_POLICY_STATE.cron;
			policyTimezone = DEFAULT_POLICY_STATE.timezone;
			policyExcluded = DEFAULT_POLICY_STATE.excludedProjects;
			policyMax = DEFAULT_POLICY_STATE.maxPerRun;
			policySkipRunning = DEFAULT_POLICY_STATE.skipWhenRunning;
			policyWindow = DEFAULT_POLICY_STATE.window;
			policyIdleMinutes = DEFAULT_POLICY_STATE.idleMinutes;
			policyLastOutcome = 'No runs yet';
			policySaving = false;
			policyOpen = false;
		}
		if (active && !hasLoaded) {
			hasLoaded = true;
			loadMods();
			loadPolicy();
		}
	});

	// A refresh can replace or withdraw the candidate an operator selected. Drop
	// only what the refresh proves stale, so a provider outage cannot discard
	// intent: a plain reload has no previews and only drops deleted rows, a check
	// clears a changed available candidate, and a row that is merely no longer
	// available stays selected but unstageable until the reviewed one returns.
	$effect(() => {
		const prune = pruneUpdateSelections(
			mods,
			selectedUpdates,
			selectedUpdateFingerprints,
			previewsSupplied
		);
		if (prune.changed.length === 0 && prune.missing.length === 0) return;
		selectedUpdates = prune.kept;
		selectedUpdateFingerprints = prune.fingerprints;
		if (prune.changed.length > 0) {
			const files = prune.changed.map((id) => mods.find((mod) => mod.id === id)?.fileName ?? id);
			selectionNotice = `Selection cleared for ${files.join(', ')}: the installed file or offered release changed; review and select it again.`;
		}
	});

	let filteredMods = $derived.by(() => {
		let result = [...mods];
		if (modFilter === 'enabled') {
			result = result.filter((m) => m.enabled);
		} else if (modFilter === 'disabled') {
			result = result.filter((m) => !m.enabled);
		}

		if (searchQuery.trim()) {
			const q = searchQuery.toLowerCase().trim();
			result = result.filter(
				(m) =>
					m.displayName.toLowerCase().includes(q) ||
					m.fileName.toLowerCase().includes(q) ||
					(m.description && m.description.toLowerCase().includes(q))
			);
		}
		return result;
	});

	function startUpdateCheck() {
		if (checking || loading || uploading || !mods.length) return;
		void loadMods(true);
	}

	async function loadMods(checkUpdates = false) {
		const serverId = server.id;
		const request = ++requestNumber;
		const isCurrent = () => request === requestNumber && server.id === serverId;
		// The check trigger keeps focus while a check runs: it is only
		// aria-disabled, never natively disabled, so Enter/Space do not send a
		// keyboard user back to the top of the page. Restore focus defensively
		// if an unrelated path did drop it to the document.
		const focused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
		checking = checkUpdates;
		updateStatus = checkUpdates ? 'Checking installed files for compatible releases…' : '';
		// A vanilla server has no mods directory, and asking for one returns an
		// error the RPC interceptor turns into a toast - on a tab that already
		// explains the server cannot take mods. Do not make the call.
		if (!canHaveMods()) {
			mods = [];
			previewsSupplied = false;
			loading = false;
			checking = false;
			return;
		}
		try {
			// A check keeps the current rows visible; only the first load (or a
			// mutation-triggered refresh) replaces them with the loading state.
			if (!checkUpdates) loading = true;
			const response = await rpcClient.mod.listMods({ serverId, checkUpdates });
			if (!isCurrent()) return;
			mods = response.mods;
			previewsSupplied = checkUpdates;
			if (checkUpdates) {
				updateStatus = summarizeModUpdateCheck(mods.map((mod) => mod.updatePreview?.state));
			}
		} catch (_e) {
			if (isCurrent()) {
				updateStatus = checkUpdates ? 'Update check failed. Try again.' : '';
				toast.error(checkUpdates ? 'Failed to check for updates' : 'Failed to load mods');
			}
		} finally {
			if (isCurrent()) {
				loading = false;
				checking = false;
				if (focused && !document.activeElement?.isConnected) focused.focus();
				else if (focused && document.activeElement === document.body) focused.focus();
			}
		}
	}

	// Sonner anchors toasts to the bottom-right of the viewport, which is where
	// the apply bar sits on a phone. A refusal toast then covered the Stage
	// button. The Stage-path notices go to the top on narrow screens; desktop
	// keeps the app default position.
	function toastStageOutcome(message: string) {
		const narrow =
			typeof window !== 'undefined' &&
			typeof window.matchMedia === 'function' &&
			window.matchMedia('(max-width: 639px)').matches;
		toast.error(message, narrow ? { position: 'top-center' } : undefined);
	}

	async function stageSelectedUpdates() {
		const selections = buildUpdateSelections(
			mods,
			currentSelectedUpdates,
			selectedUpdateFingerprints
		);
		if (selections.length === 0) return;

		let applyAtUnix = 0;
		if (updateWindow === 'time') {
			const parsed = new Date(applyAtLocal).getTime();
			if (!applyAtLocal || !Number.isFinite(parsed)) {
				toastStageOutcome('Choose a valid apply time');
				return;
			}
			applyAtUnix = Math.floor(parsed / 1000);
		}
		if (updateWindow === 'idle' && (!Number.isInteger(idleMinutes) || idleMinutes <= 0)) {
			toastStageOutcome('Idle minutes must be a positive whole number');
			return;
		}

		try {
			stagingUpdates = true;
			const response = await rpcClient.mod.updateMod({
				serverId: server.id,
				updateSelections: selections,
				updateWindow: applyWindowEnum(),
				updateApplyAtUnix: BigInt(applyAtUnix),
				updateIdleMinutes: updateWindow === 'idle' ? idleMinutes : 0
			});
			const staged = response.stagedUpdates.filter((row) => row.outcome === 'staged').length;
			const refused = response.stagedUpdates.length - staged;
			updateStatus = `Staged ${staged} update(s) for the pending batch${refused ? `; ${refused} refused and left untouched` : ''}.`;
			selectionNotice = '';
			selectedUpdates = [];
			selectedUpdateFingerprints = {};
			if (staged > 0) {
				toast.success(`${staged} mod update(s) staged`);
				await loadMods(true);
			} else {
				toastStageOutcome('No update was staged. Recheck the installed files and try again.');
			}
		} catch (error) {
			if (!wasRpcErrorToasted(error)) toastStageOutcome('Failed to stage mod updates');
		} finally {
			stagingUpdates = false;
		}
	}

	async function handleFileSelect(event: Event) {
		const input = event.target as HTMLInputElement;
		const fileList = input.files;
		if (!fileList || fileList.length === 0) return;

		uploading = true;
		uploadAbortController = new AbortController();

		try {
			for (const file of Array.from(fileList)) {
				currentUploadFilename = file.name;
				uploadProgress = null;

				const result = await uploadFile(file, {
					onProgress: (progress) => {
						uploadProgress = progress;
					},
					signal: uploadAbortController.signal
				});

				await rpcClient.mod.importUploadedMod({
					serverId: server.id,
					uploadSessionId: result.sessionId,
					displayName: file.name,
					description: ''
				});
			}
			toast.success(`Uploaded ${fileList.length} mod(s)`);
			await loadMods();
		} catch (error: unknown) {
			if (error instanceof Error && error.message === 'Upload cancelled') {
				toast.info('Upload cancelled');
			} else {
				toast.error('Failed to upload mod');
			}
		} finally {
			uploading = false;
			uploadProgress = null;
			currentUploadFilename = '';
			uploadAbortController = null;
			input.value = '';
		}
	}

	function cancelCurrentUpload() {
		if (uploadAbortController) {
			uploadAbortController.abort();
		}
		if (uploadProgress?.sessionId) {
			cancelUpload(uploadProgress.sessionId).catch(() => {});
		}
	}

	async function toggleMod(mod: Mod) {
		try {
			await rpcClient.mod.updateMod({
				serverId: server.id,
				modId: mod.id,
				enabled: !mod.enabled,
				displayName: mod.displayName,
				description: mod.description
			});
			toast.success(`Mod ${!mod.enabled ? 'enabled' : 'disabled'}`);
			await loadMods();
		} catch (_e) {
			toast.error('Failed to toggle mod');
		}
	}

	let pendingDelete = $state<Mod | null>(null);
	let confirmOpen = $state(false);

	async function deleteMod(mod: Mod) {
		pendingDelete = mod;
		confirmOpen = true;
	}

	async function confirmDeleteMod() {
		const mod = pendingDelete;
		if (!mod) return;
		try {
			await rpcClient.mod.deleteMod({
				serverId: server.id,
				modId: mod.id
			});
			toast.success('Mod deleted');
			await loadMods();
		} catch (_e) {
			toast.error('Failed to delete mod');
		} finally {
			pendingDelete = null;
		}
	}

	async function downloadMod(mod: Mod) {
		try {
			const response = await rpcClient.file.getFile({
				serverId: server.id,
				path: `${getModsDirectory()}/${mod.fileName}`
			});
			const blob = new Blob([new Uint8Array(response.content)]);
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = mod.fileName;
			a.click();
			URL.revokeObjectURL(url);
		} catch (_e) {
			toast.error('Failed to download mod');
		}
	}

	function getModsDirectory(): string {
		const modLoaderInfo: Record<ModLoader, string> = {
			[ModLoader.UNSPECIFIED]: 'mods',
			[ModLoader.VANILLA]: 'mods',
			[ModLoader.FORGE]: 'mods',
			[ModLoader.NEOFORGE]: 'mods',
			[ModLoader.FABRIC]: 'mods',
			[ModLoader.QUILT]: 'mods',
			[ModLoader.BUKKIT]: 'plugins',
			[ModLoader.SPIGOT]: 'plugins',
			[ModLoader.PAPER]: 'plugins',
			[ModLoader.PURPUR]: 'plugins',
			[ModLoader.SPONGE_VANILLA]: 'mods',
			[ModLoader.SPONGE_FORGE]: 'mods',
			[ModLoader.MOHIST]: 'mods',
			[ModLoader.CATSERVER]: 'mods',
			[ModLoader.ARCLIGHT]: 'mods',
			[ModLoader.AUTO_CURSEFORGE]: 'mods',
			[ModLoader.MODRINTH]: 'mods',
			[ModLoader.FOLIA]: 'plugins'
		};

		return modLoaderInfo[server.modLoader] || 'mods';
	}

	function canHaveMods(): boolean {
		const noModLoaders = [ModLoader.VANILLA, ModLoader.UNSPECIFIED];
		return !noModLoaders.includes(server.modLoader);
	}
</script>

<ServerTabPanel
	title="Mod & Plugin Management"
	description={canHaveMods()
		? `Manage extensions in the container ${getModsDirectory()} directory`
		: 'Vanilla Minecraft does not support external mod injection'}
>
	{#snippet chip()}
		<CarbonTag type="teal" size="sm">{getModsDirectory()}/</CarbonTag>
	{/snippet}
	{#snippet actions()}
		{#if canHaveMods()}
			<!--
				Icon-only below sm. The full label otherwise wraps onto a third
				action row at 390 px and takes the height the mod list needs. One
				element only, so the tab order and accessible name stay stable.
			-->
			<CarbonButton
				kind="secondary"
				size="sm"
				class="max-sm:px-2! {checking ? 'opacity-50' : ''}"
				disabled={loading || uploading || !mods.length}
				aria-disabled={checking ? 'true' : undefined}
				aria-label="Check for updates"
				title="Check for updates"
				onclick={startUpdateCheck}
			>
				<RefreshCw class="h-4 w-4 sm:hidden {checking ? 'animate-spin' : ''}" />
				<span class="hidden sm:inline">Check for updates</span>
			</CarbonButton>
			<CarbonButton kind="primary" size="sm" onclick={() => (browserDialogOpen = true)}>
				<Boxes class="h-4 w-4" />
				<!--
					Short labels below sm keep all four action buttons on one
					row; a wrapped policy trigger cost the list another 40 px.
				-->
				<span class="sm:hidden">Browse</span>
				<span class="hidden sm:inline">Browse Mods</span>
			</CarbonButton>
			<CarbonButton
				kind="secondary"
				size="sm"
				onclick={() => fileInput?.click()}
				disabled={uploading}
			>
				{#if uploading}
					<Loader2 class="h-4 w-4 animate-spin" />
				{:else}
					<Upload class="h-4 w-4" />
				{/if}
				<span class="sm:hidden">Upload</span>
				<span class="hidden sm:inline">Upload .jar</span>
			</CarbonButton>
			<!--
				The policy used to be an inline disclosure. Its form is taller than
				the pane on a phone, which collapsed the list to 0 px and clipped
				the fields. It now opens in a dialog; the narrow trigger is
				icon-only with a stable accessible name, like Check.
			-->
			<CarbonButton
				kind="secondary"
				size="sm"
				class="max-sm:px-2!"
				aria-label="Automatic update policy"
				title="Automatic update policy"
				onclick={() => (policyOpen = true)}
			>
				<CalendarClock class="h-4 w-4 sm:hidden" />
				<span class="hidden sm:inline">Automatic update policy</span>
			</CarbonButton>
			<input
				bind:this={fileInput}
				type="file"
				multiple
				accept=".jar,.zip"
				onchange={handleFileSelect}
				class="hidden"
			/>
		{/if}
	{/snippet}
	<p role="status" class="px-3 text-sm text-muted-foreground">{updateStatus}</p>
	{#if selectionNotice}
		<p role="status" class="px-3 text-sm text-warning-text">{selectionNotice}</p>
	{/if}
	{#if availableUpdates.length > 0}
		<!--
			One compact control per row on a phone: an inline label beside the
			input keeps the three window choices and their inputs, while the
			Stage action gets its own full-width row. The stacked desktop form
			(sm+) keeps the labels above their inputs.
		-->
		<div
			class="flex flex-col gap-2 border border-border bg-card p-2 sm:flex-row sm:items-end sm:gap-3 sm:p-3"
		>
			<div class="flex min-w-0 flex-1 items-center gap-2 sm:block sm:flex-none">
				<label
					for="mod-update-window"
					class="shrink-0 font-mono text-xs text-muted-foreground uppercase sm:mb-1 sm:block"
				>
					Apply window
				</label>
				<select
					id="mod-update-window"
					bind:value={updateWindow}
					class="h-8 min-w-0 flex-1 rounded-none border border-border bg-background px-2 text-xs text-foreground focus:border-primary focus:outline-none sm:flex-none"
				>
					<option value="next">Next restart</option>
					<option value="time">At a chosen time (browser local)</option>
					<option value="idle">When the server is empty</option>
				</select>
			</div>
			{#if updateWindow === 'time'}
				<div class="flex min-w-0 flex-1 items-center gap-2 sm:block sm:flex-none">
					<label
						for="mod-update-apply-at"
						class="shrink-0 font-mono text-xs text-muted-foreground uppercase sm:mb-1 sm:block"
					>
						Apply at
					</label>
					<input
						id="mod-update-apply-at"
						type="datetime-local"
						bind:value={applyAtLocal}
						class="h-8 min-w-0 flex-1 rounded-none border border-border bg-background px-2 text-xs text-foreground focus:border-primary focus:outline-none sm:flex-none"
					/>
				</div>
			{/if}
			{#if updateWindow === 'idle'}
				<div class="flex min-w-0 flex-1 items-center gap-2 sm:block sm:flex-none">
					<label
						for="mod-update-idle"
						class="shrink-0 font-mono text-xs text-muted-foreground uppercase sm:mb-1 sm:block"
					>
						Idle minutes
					</label>
					<input
						id="mod-update-idle"
						type="number"
						min="1"
						bind:value={idleMinutes}
						class="h-8 w-24 rounded-none border border-border bg-background px-2 text-xs text-foreground focus:border-primary focus:outline-none"
					/>
				</div>
			{/if}
			<CarbonButton
				kind="primary"
				size="sm"
				class="w-full justify-center sm:w-auto"
				disabled={stagingUpdates || currentSelectedUpdates.length === 0}
				onclick={stageSelectedUpdates}
			>
				Stage {currentSelectedUpdates.length} selected update{currentSelectedUpdates.length === 1
					? ''
					: 's'}
			</CarbonButton>
		</div>
	{/if}
	{#snippet toolbar()}
		{#if canHaveMods()}
			<div
				class="flex flex-col items-stretch justify-between gap-3 p-3 sm:flex-row sm:items-center"
			>
				<div class="relative max-w-md flex-1">
					<Search
						class="absolute top-1/2 left-3 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground"
					/>
					<input
						type="search"
						placeholder="Search installed mods..."
						bind:value={searchQuery}
						onkeydown={(e) => e.key === 'Escape' && (searchQuery = '')}
						class="h-8 w-full rounded-none border-b border-border bg-card pr-3 pl-8 text-xs text-foreground placeholder-muted-foreground focus:border-b-2 focus:border-primary focus:outline-none"
					/>
				</div>
				<div class="flex items-center gap-1">
					<button
						type="button"
						onclick={() => (modFilter = 'all')}
						aria-pressed={modFilter === 'all'}
						class="cursor-pointer rounded-none border px-2.5 py-1 font-mono text-xs uppercase transition-colors {modFilter ===
						'all'
							? 'border-primary bg-primary text-white'
							: 'border-border bg-card text-muted-foreground hover:bg-secondary'}"
					>
						All ({mods.length})
					</button>
					<button
						type="button"
						onclick={() => (modFilter = 'enabled')}
						aria-pressed={modFilter === 'enabled'}
						class="cursor-pointer rounded-none border px-2.5 py-1 font-mono text-xs uppercase transition-colors {modFilter ===
						'enabled'
							? 'border-[#198038] bg-[#198038] text-white'
							: 'border-success/50 bg-card text-success-text hover:bg-secondary'}"
					>
						Enabled ({mods.filter((m) => m.enabled).length})
					</button>
					<button
						type="button"
						onclick={() => (modFilter = 'disabled')}
						aria-pressed={modFilter === 'disabled'}
						class="cursor-pointer rounded-none border px-2.5 py-1 font-mono text-xs uppercase transition-colors {modFilter ===
						'disabled'
							? 'border-border bg-secondary text-foreground'
							: 'border-border/60 bg-card text-muted-foreground hover:bg-secondary'}"
					>
						Disabled ({mods.filter((m) => !m.enabled).length})
					</button>
				</div>
			</div>
		{/if}
		{#if uploading && uploadProgress}
			<div class="border-t border-border bg-card p-3 text-xs">
				<div class="mb-1 flex items-center justify-between">
					<span class="truncate font-mono text-foreground">Uploading: {currentUploadFilename}</span>
					<div class="flex items-center gap-2">
						<span class="font-mono text-accent-text"
							>{uploadProgress.percentComplete.toFixed(0)}%</span
						>
						<button
							type="button"
							onclick={cancelCurrentUpload}
							class="p-0.5 text-destructive-text hover:text-foreground"
							title="Cancel upload"
							aria-label="Cancel upload"
						>
							<X class="h-3.5 w-3.5" />
						</button>
					</div>
				</div>
				<div class="h-1.5 w-full rounded-none border border-border bg-background">
					<div
						class="h-full rounded-none bg-primary transition-all"
						style="width: {uploadProgress.percentComplete}%"
					></div>
				</div>
				<p class="mt-1 font-mono text-xs text-muted-foreground">
					{formatBytes(uploadProgress.bytesUploaded)} / {formatBytes(uploadProgress.totalBytes)}
				</p>
			</div>
		{/if}
	{/snippet}
	<div class="min-h-0 flex-1 overflow-auto bg-background" aria-busy={checking ? 'true' : undefined}>
		{#if !canHaveMods()}
			<div class="flex flex-col items-center justify-center p-12 text-center text-muted-foreground">
				<Package class="mb-3 h-10 w-10 text-muted-foreground" />
				<p class="text-sm font-semibold text-foreground">Mod loader incompatible</p>
				<p class="mt-1 max-w-sm text-xs text-muted-foreground">
					To install mods or plugins, configure this server to use Fabric, Forge, NeoForge, Paper,
					or Purpur.
				</p>
			</div>
		{:else if loading}
			<div class="flex items-center justify-center p-12">
				<CarbonInlineLoading description="Reading {getModsDirectory()}/ directory..." />
			</div>
		{:else if mods.length === 0}
			<div class="flex flex-col items-center justify-center p-12 text-center text-muted-foreground">
				<Package class="mb-3 h-10 w-10 text-muted-foreground" />
				<p class="text-sm font-semibold text-foreground">No mods installed</p>
				<p class="mt-1 text-xs text-muted-foreground">
					Browse the repository or upload .jar files directly to get started.
				</p>
			</div>
		{:else if filteredMods.length === 0}
			<div class="p-8 text-center text-xs text-muted-foreground">
				No mods match your search query or filter.
			</div>
		{:else}
			<!-- Carbon DataTable rows -->
			<div class="divide-y divide-border border-b border-border">
				{#each filteredMods as mod (mod.id)}
					<div
						class="group flex items-center justify-between gap-4 rounded-none bg-card p-3.5 transition-colors hover:bg-secondary"
					>
						<div class="flex min-w-0 items-center gap-3.5">
							<!-- Enable/Disable Carbon Toggle -->
							<button
								type="button"
								onclick={() => toggleMod(mod)}
								class="flex h-7 cursor-pointer items-center gap-1 rounded-none border px-2 font-mono text-xs transition-colors select-none {mod.enabled
									? 'border-success bg-success/20 text-success-text'
									: 'border-border bg-background text-muted-foreground'}"
								title={mod.enabled ? 'Click to disable' : 'Click to enable'}
							>
								{#if mod.enabled}
									<Check class="h-3 w-3" />
									<span class="text-xs uppercase">Active</span>
								{:else}
									<Ban class="h-3 w-3" />
									<span class="text-xs uppercase">Disabled</span>
								{/if}
							</button>

							<!-- Info -->
							<div class="min-w-0">
								<div class="flex items-center gap-2">
									<h4
										class="truncate text-sm font-medium text-foreground {mod.enabled
											? ''
											: 'line-through opacity-70'}"
									>
										{mod.displayName}
									</h4>
									{#if mod.version}
										<span
											class="border border-border bg-background px-1.5 py-0 font-mono text-xs text-muted-foreground"
										>
											{mod.version}
										</span>
									{/if}
								</div>
								<div
									class="mt-0.5 flex flex-wrap items-center gap-3 font-mono text-xs text-muted-foreground"
								>
									<span class="flex items-center gap-1 text-muted-foreground">
										<FileText class="h-3 w-3" />
										{mod.fileName}
									</span>
									<span>{formatBytes(Number(mod.fileSize))}</span>
									{#if mod.uploadedAt}
										<span
											>{new Date(Number(mod.uploadedAt.seconds) * 1000).toLocaleDateString()}</span
										>
									{/if}
								</div>
								{#if mod.updatePreview}
									<div class="mt-1 flex flex-wrap items-center gap-2">
										<span
											class="border px-1.5 py-0 font-mono text-xs uppercase {updateStateClass(
												mod.updatePreview.state
											)}"
										>
											{mod.updatePreview.state.replace(/_/g, ' ')}
										</span>
										{#if mod.updatePreview.state === 'available'}
											<label
												class="flex cursor-pointer items-center gap-1.5 text-xs text-foreground"
											>
												<input
													type="checkbox"
													checked={currentSelectedUpdates.includes(mod.id)}
													onchange={() => toggleUpdateSelection(mod)}
													class="h-3.5 w-3.5 accent-primary"
												/>
												Stage this update
											</label>
										{/if}
									</div>
									{#if mod.updatePreview.installedVersion}
										<p class="mt-1 text-xs break-words text-muted-foreground">
											Modrinth · Installed {mod.updatePreview.installedVersion}
											{#if mod.updatePreview.candidateVersion && mod.updatePreview.candidateVersionId !== mod.updatePreview.installedVersionId}
												· Candidate {mod.updatePreview.candidateVersion}
											{/if}
										</p>
									{/if}
									<p class="mt-1 text-sm text-muted-foreground">{mod.updatePreview.reason}</p>
								{/if}
								{#if mod.description}
									<p class="mt-1 line-clamp-1 font-sans text-xs text-muted-foreground">
										{mod.description}
									</p>
								{/if}
							</div>
						</div>

						<!-- Actions -->
						<div class="flex shrink-0 items-center border border-border bg-background">
							<button
								type="button"
								onclick={() => downloadMod(mod)}
								class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none border-r border-border text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
								title="Download .jar"
							>
								<Download class="h-3.5 w-3.5" />
							</button>
							<button
								type="button"
								onclick={() => deleteMod(mod)}
								class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none text-destructive-text transition-colors hover:bg-destructive/20"
								title="Delete mod"
							>
								<Trash2 class="h-3.5 w-3.5" />
							</button>
						</div>
					</div>
				{/each}
			</div>
		{/if}
	</div>
</ServerTabPanel>

<!--
	The policy editor is a dialog at every width. Inline, the form (555 px on a
	phone) consumed the fixed-height pane, set the installed-mod list to 0 px and
	was clipped by the pane's overflow, so pointer users could not reach Save.
	Fields, opt-in semantics and the schedule/run status are unchanged.
-->
{#if canHaveMods()}
	<CarbonModal
		bind:open={policyOpen}
		title="Automatic update policy"
		description="Stage compatible updates on a schedule for this server"
		size="lg"
		onclose={() => (policyOpen = false)}
	>
		<div class="flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-end">
			<label class="flex w-full items-center gap-2 text-xs text-foreground">
				<input type="checkbox" bind:checked={policyEnabled} class="h-3.5 w-3.5 accent-primary" />
				Opt in to automatic updates
			</label>
			<div class="min-w-0">
				<label
					for="mod-policy-cron"
					class="mb-1 block font-mono text-xs text-muted-foreground uppercase"
				>
					Schedule (cron)
				</label>
				<input
					id="mod-policy-cron"
					bind:value={policyCron}
					class="h-8 w-40 rounded-none border border-border bg-background px-2 font-mono text-xs text-foreground focus:border-primary focus:outline-none"
				/>
			</div>
			<div class="min-w-0">
				<label
					for="mod-policy-tz"
					class="mb-1 block font-mono text-xs text-muted-foreground uppercase"
				>
					Timezone
				</label>
				<input
					id="mod-policy-tz"
					bind:value={policyTimezone}
					class="h-8 w-32 rounded-none border border-border bg-background px-2 text-xs text-foreground focus:border-primary focus:outline-none"
				/>
			</div>
			<div class="min-w-0">
				<label
					for="mod-policy-excluded"
					class="mb-1 block font-mono text-xs text-muted-foreground uppercase"
				>
					Excluded project IDs
				</label>
				<input
					id="mod-policy-excluded"
					bind:value={policyExcluded}
					placeholder="project-a, project-b"
					class="h-8 w-48 rounded-none border border-border bg-background px-2 text-xs text-foreground focus:border-primary focus:outline-none"
				/>
			</div>
			<div class="min-w-0">
				<label
					for="mod-policy-max"
					class="mb-1 block font-mono text-xs text-muted-foreground uppercase"
				>
					Max per run
				</label>
				<input
					id="mod-policy-max"
					type="number"
					min="1"
					max="50"
					bind:value={policyMax}
					class="h-8 w-20 rounded-none border border-border bg-background px-2 text-xs text-foreground focus:border-primary focus:outline-none"
				/>
			</div>
			<div class="min-w-0">
				<label
					for="mod-policy-window"
					class="mb-1 block font-mono text-xs text-muted-foreground uppercase"
				>
					Apply window
				</label>
				<select
					id="mod-policy-window"
					bind:value={policyWindow}
					class="h-8 rounded-none border border-border bg-background px-2 text-xs text-foreground focus:border-primary focus:outline-none"
				>
					<option value="next_restart">Next restart</option>
					<option value="idle">When empty</option>
				</select>
			</div>
			{#if policyWindow === 'idle'}
				<div class="min-w-0">
					<label
						for="mod-policy-idle"
						class="mb-1 block font-mono text-xs text-muted-foreground uppercase"
					>
						Idle minutes
					</label>
					<input
						id="mod-policy-idle"
						type="number"
						min="1"
						max="10080"
						bind:value={policyIdleMinutes}
						class="h-8 w-24 rounded-none border border-border bg-background px-2 text-xs text-foreground focus:border-primary focus:outline-none"
					/>
				</div>
			{/if}
			<label class="flex items-center gap-2 text-xs text-foreground">
				<input
					type="checkbox"
					bind:checked={policySkipRunning}
					class="h-3.5 w-3.5 accent-primary"
				/>
				Skip while running
			</label>
		</div>
		{#if policyLoading && !policyReady}
			<CarbonInlineLoading description="Loading policy" />
		{:else if !policyReady}
			<CarbonButton kind="ghost" size="sm" onclick={loadPolicy}>Retry loading policy</CarbonButton>
		{/if}
		<dl
			class="grid grid-cols-1 gap-1 border-t border-border pt-3 font-mono text-xs text-muted-foreground sm:grid-cols-2"
		>
			<div>
				<dt class="inline">Schedule:</dt>
				<dd class="inline">{policyScheduleStatus}</dd>
			</div>
			<div>
				<dt class="inline">Next run:</dt>
				<dd class="inline">{policyNextRun}</dd>
			</div>
			<div>
				<dt class="inline">Last run:</dt>
				<dd class="inline">{policyLastRun}</dd>
			</div>
			<div>
				<dt class="inline">Outcome:</dt>
				<dd class="inline break-words">{policyLastOutcome}</dd>
			</div>
		</dl>

		{#snippet footer()}
			<div class="flex w-full">
				<CarbonButton
					kind="secondary"
					size="lg"
					class="w-1/2 justify-center rounded-none"
					onclick={() => (policyOpen = false)}
				>
					Close
				</CarbonButton>
				<CarbonButton
					kind="primary"
					size="lg"
					class="w-1/2 justify-center rounded-none"
					disabled={!policyReady || policyLoading || policySaving}
					onclick={savePolicy}
				>
					Save policy
				</CarbonButton>
			</div>
		{/snippet}
	</CarbonModal>
{/if}

<ModBrowserDialog bind:open={browserDialogOpen} {server} onInstalled={loadMods} />
<CarbonConfirm
	bind:open={confirmOpen}
	title="Delete mod?"
	message="The file is removed from the server. This cannot be undone."
	confirmLabel="Delete Mod"
	danger
	onconfirm={confirmDeleteMod}
	onclose={() => (pendingDelete = null)}
>
	{#snippet details()}
		{#if pendingDelete}{pendingDelete.displayName}{/if}
	{/snippet}
</CarbonConfirm>
