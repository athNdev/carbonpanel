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
	import { Upload, Download, Trash2, FileText, X, Boxes, Search, Check, Ban } from '@lucide/svelte';
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
		updateOutcomeLabel,
		updateOutcomeTagType,
		updateCandidateFingerprint,
		updateStateTagType
	} from '$lib/mod-update-ui';
	import { uploadFile, cancelUpload, type UploadProgress } from '$lib/utils/chunked-upload';
	import ModBrowserDialog from '$lib/components/mod-browser-dialog.svelte';
	import {
		CarbonTag,
		CarbonInlineLoading,
		CarbonButton,
		CarbonConfirm,
		CarbonSpinner,
		CarbonDataTable,
		CarbonAccordion,
		CarbonAccordionItem,
		CarbonEmptyState,
		CarbonErrorState
	} from '$lib/components/carbon';
	import ServerTabPanel from '$lib/components/server-ui/ServerTabPanel.svelte';

	interface Props {
		server: Server;
		active?: boolean;
	}

	let { server, active = false }: Props = $props();

	let mods = $state<Mod[]>([]);
	let loading = $state(true);
	// E1: a failed load must never render as the "No mods installed" success
	// state; the error carries its own retry affordance.
	let loadError = $state('');
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
	// Status-strip ownership (E5): update checks take a token and staging
	// invalidates outstanding ones, so an older in-flight check can never
	// overwrite a newer stage result (e.g. a refused stage) on the strip.
	let statusNumber = 0;
	let updateStatus = $state('');
	let previousServerId = $state(server.id);

	// E3: per-row pending state so double-clicks cannot double-apply.
	let busyMods = $state<Record<string, 'toggle' | 'delete'>>({});

	// Reviewed update application. Selection is explicit: a preview never
	// stages bytes on its own, and only state=available rows can be selected.
	let selectedUpdates = $state<string[]>([]);
	let selectedUpdateFingerprints = $state<Record<string, string>>({});
	let updateWindow = $state<'next' | 'time' | 'idle'>('next');
	let applyAtLocal = $state('');
	let idleMinutes = $state(15);
	let stagingUpdates = $state(false);
	let checkingUpdates = $state(false);
	let stagedOutcomes = $state<StagedOutcomeRow[]>([]);
	let policyOpen = $state(false);

	// Opt-in automatic policy. The policy is a typed mod_update task; TaskService
	// owns schedule, timezone, next/last run and bounded retries.
	let policyTask = $state<ScheduledTask | null>(null);
	let policyEnabled = $state(false);
	let policyCron = $state('0 3 * * *');
	let policyTimezone = $state('UTC');
	let policyExcluded = $state('');
	let policyMax = $state(5);
	let policySkipRunning = $state(true);
	let policySkipRunningExplicit = $state(true);
	let policyWindow = $state<'next_restart' | 'idle'>('next_restart');
	let policyIdleMinutes = $state(15);
	let policyLastOutcome = $state('No runs yet');
	let policySaving = $state(false);
	let policyLoading = $state(false);
	let policyLoadedServer = $state<string | null>(null);
	let policyReady = $derived(policyLoadedServer === server.id);
	let loadedPolicyOptIn = false;
	let policyGeneration = 0;
	onDestroy(() => {
		policyGeneration++;
	});

	interface StagedOutcomeRow {
		modId: string;
		fileName: string;
		outcome: string;
		reason: string;
	}

	let availableUpdates = $derived(mods.filter((mod) => mod.updatePreview?.state === 'available'));
	let currentSelectedUpdates = $derived(
		currentUpdateSelectionIds(mods, selectedUpdates, selectedUpdateFingerprints)
	);

	$effect(() => {
		if (currentSelectedUpdates.length === selectedUpdates.length) return;
		selectedUpdates = currentSelectedUpdates;
		selectedUpdateFingerprints = Object.fromEntries(
			currentSelectedUpdates.map((id) => [id, selectedUpdateFingerprints[id]])
		);
	});

	function toggleUpdateSelection(id: string) {
		const mod = mods.find((candidate) => candidate.id === id);
		if (!mod?.updatePreview || mod.updatePreview.state !== 'available') return;
		const fingerprint = updateCandidateFingerprint(mod.updatePreview);
		if (currentSelectedUpdates.includes(id)) {
			selectedUpdates = selectedUpdates.filter((selected) => selected !== id);
			const fingerprints = { ...selectedUpdateFingerprints };
			delete fingerprints[id];
			selectedUpdateFingerprints = fingerprints;
		} else {
			selectedUpdates = [...selectedUpdates.filter((selected) => selected !== id), id];
			selectedUpdateFingerprints = { ...selectedUpdateFingerprints, [id]: fingerprint };
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
	let policySummary = $derived(`${policyScheduleStatus} · Next run: ${policyNextRun}`);

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
			policySkipRunning = state.skipWhenRunning ?? false;
			policySkipRunningExplicit = state.skipWhenRunning !== undefined || !task;
			policyWindow = state.window;
			policyIdleMinutes = state.idleMinutes;
			if (task) {
				const history = await rpcClient.task.listTaskExecutions({ taskId: task.id, limit: 1 });
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
			skipWhenRunning: policySkipRunningExplicit ? policySkipRunning : undefined,
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
					description: 'Stages compatible provider updates into the pending batch.',
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
			// E4: an in-flight upload must not import the old file into the new
			// server. Aborting and cancelling the session happens before the
			// state reset; the upload flow also re-checks the target server id.
			cancelCurrentUpload();
			updateStatus = '';
			checkingUpdates = false;
			selectedUpdates = [];
			selectedUpdateFingerprints = {};
			stagedOutcomes = [];
			mods = [];
			loading = true;
			loadError = '';
			uploading = false;
			uploadProgress = null;
			currentUploadFilename = '';
			uploadAbortController = null;
			busyMods = {};
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
			policySkipRunning = DEFAULT_POLICY_STATE.skipWhenRunning ?? true;
			policySkipRunningExplicit = true;
			policyWindow = DEFAULT_POLICY_STATE.window;
			policyIdleMinutes = DEFAULT_POLICY_STATE.idleMinutes;
			policyLastOutcome = 'No runs yet';
			policySaving = false;
		}
		if (active && !hasLoaded) {
			hasLoaded = true;
			loadMods();
			loadPolicy();
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

	async function loadMods(checkUpdates = false) {
		const serverId = server.id;
		const request = ++requestNumber;
		const isCurrent = () => request === requestNumber && server.id === serverId;
		// Only a check writes the status strip; plain refreshes never do.
		const statusToken = checkUpdates ? ++statusNumber : 0;
		const ownsStatus = () => statusToken !== 0 && statusToken === statusNumber;
		// E5: checking and staging are mutually exclusive. A check never
		// clobbers an in-flight staging summary; the stage flow owns
		// updateStatus until it finishes.
		if (checkUpdates && !stagingUpdates) {
			updateStatus = 'Checking installed files for compatible releases…';
		}
		checkingUpdates = checkUpdates;
		// A vanilla server has no mods directory, and asking for one returns an
		// error the RPC interceptor turns into a toast - on a tab that already
		// explains the server cannot take mods. Do not make the call.
		if (!canHaveMods()) {
			mods = [];
			loading = false;
			return;
		}
		try {
			loading = true;
			// checkUpdates hashes every installed jar server-side, so it only
			// rides along when the operator explicitly asks for a check.
			const response = await rpcClient.mod.listMods({ serverId, checkUpdates });
			if (!isCurrent()) return;
			mods = response.mods;
			loadError = '';
			if (checkUpdates && !stagingUpdates && ownsStatus()) {
				const available = mods.filter((mod) => mod.updatePreview?.state === 'available').length;
				updateStatus = `Checked ${mods.length} installed files. Compatible updates available for ${available}.`;
			}
		} catch (error) {
			if (isCurrent()) {
				// E1: record the failure so the error state renders instead of the
				// empty state, with a retry affordance.
				loadError = checkUpdates
					? 'The update check failed. The installed list was not refreshed.'
					: 'Failed to read the installed mods from the server.';
				if (checkUpdates && !stagingUpdates && ownsStatus())
					updateStatus = 'Update check failed. Try again.';
				// E2: the interceptor already toasted the formatted RPC error.
				if (!wasRpcErrorToasted(error))
					toast.error(checkUpdates ? 'Failed to check for updates' : 'Failed to load mods');
			}
		} finally {
			if (isCurrent()) {
				loading = false;
				checkingUpdates = false;
			}
		}
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
				toast.error('Choose a valid apply time');
				return;
			}
			applyAtUnix = Math.floor(parsed / 1000);
		}
		if (updateWindow === 'idle' && (!Number.isInteger(idleMinutes) || idleMinutes <= 0)) {
			toast.error('Idle minutes must be a positive whole number');
			return;
		}

		try {
			// E5: staging takes over the status strip from any running check.
			stagingUpdates = true;
			checkingUpdates = false;
			// Invalidate outstanding check tokens: an older in-flight check
			// must not overwrite this stage's result once it lands.
			statusNumber++;
			stagedOutcomes = [];
			const response = await rpcClient.mod.updateMod({
				serverId: server.id,
				updateSelections: selections,
				updateWindow: applyWindowEnum(),
				updateApplyAtUnix: BigInt(applyAtUnix),
				updateIdleMinutes: updateWindow === 'idle' ? idleMinutes : 0
			});
			stagedOutcomes = response.stagedUpdates.map((row) => ({
				modId: row.modId,
				fileName: row.fileName,
				outcome: row.outcome,
				reason: row.reason
			}));
			const staged = response.stagedUpdates.filter((row) => row.outcome === 'staged').length;
			const refused = response.stagedUpdates.length - staged;
			updateStatus = `Staged ${staged} update(s) for the pending batch${refused ? `; ${refused} refused and left untouched` : ''}.`;
			selectedUpdates = [];
			selectedUpdateFingerprints = {};
			if (staged > 0) {
				toast.success(`${staged} mod update(s) staged`);
				await loadMods(true);
			} else {
				toast.error('No update was staged. Recheck the installed files and try again.');
			}
		} catch (error) {
			if (!wasRpcErrorToasted(error)) toast.error('Failed to stage mod updates');
		} finally {
			stagingUpdates = false;
		}
	}

	async function handleFileSelect(event: Event) {
		const input = event.target as HTMLInputElement;
		const fileList = input.files;
		if (!fileList || fileList.length === 0) return;

		// E4: the upload is bound to the server it was started on. A server
		// switch aborts it and the import re-checks the target before running.
		const targetServerId = server.id;
		// The batch keeps its own controller reference: a server switch tears
		// the shared state down (sets it to null), and the remaining files
		// must see the aborted signal instead of dereferencing a nulled
		// controller and reporting a misleading "failed to upload" toast.
		const batchController = new AbortController();
		uploadAbortController = batchController;
		uploading = true;

		try {
			for (const file of Array.from(fileList)) {
				if (batchController.signal.aborted) break;
				currentUploadFilename = file.name;
				uploadProgress = null;

				const result = await uploadFile(file, {
					onProgress: (progress) => {
						uploadProgress = progress;
					},
					signal: batchController.signal
				});

				if (server.id !== targetServerId || uploadAbortController === null) {
					toast.info(`Server changed — upload of ${file.name} was discarded`);
					continue;
				}
				await rpcClient.mod.importUploadedMod({
					serverId: targetServerId,
					uploadSessionId: result.sessionId,
					displayName: file.name,
					description: ''
				});
				if (server.id !== targetServerId || batchController.signal.aborted) break;
			}
			if (server.id === targetServerId && !batchController.signal.aborted) {
				toast.success(`Staged ${fileList.length} mod(s) for the next apply window`);
				await loadMods();
			}
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
		if (busyMods[mod.id]) return;
		busyMods = { ...busyMods, [mod.id]: 'toggle' };
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
		} catch (error) {
			if (!wasRpcErrorToasted(error)) toast.error('Failed to toggle mod');
		} finally {
			const { [mod.id]: _settled, ...rest } = busyMods;
			busyMods = rest;
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
		if (!mod || busyMods[mod.id]) return;
		busyMods = { ...busyMods, [mod.id]: 'delete' };
		try {
			await rpcClient.mod.deleteMod({
				serverId: server.id,
				modId: mod.id
			});
			toast.success('Mod deleted');
			await loadMods();
		} catch (error) {
			if (!wasRpcErrorToasted(error)) toast.error('Failed to delete mod');
		} finally {
			const { [mod.id]: _settled, ...rest } = busyMods;
			busyMods = rest;
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
		} catch (error) {
			if (!wasRpcErrorToasted(error)) toast.error('Failed to download mod');
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
			<CarbonButton
				kind="secondary"
				size="sm"
				disabled={loading || uploading || stagingUpdates || !mods.length}
				onclick={() => loadMods(true)}
			>
				Check for updates
			</CarbonButton>
			<CarbonButton kind="primary" size="sm" onclick={() => (browserDialogOpen = true)}>
				<Boxes class="h-4 w-4" />
				<span>Browse & Install</span>
			</CarbonButton>
			<CarbonButton
				kind="secondary"
				size="sm"
				onclick={() => fileInput?.click()}
				disabled={uploading}
			>
				{#if uploading}
					<CarbonSpinner size="sm" label="Uploading mod" />
				{:else}
					<Upload class="h-4 w-4" />
				{/if}
				<span>Upload .jar</span>
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
							? 'border-success bg-success text-white'
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
	{/snippet}

	<!-- Status strip: one place the eye checks after any operation. Upload
		progress lives inline here instead of in its own floating bar. -->
	<div class="border-y border-border bg-card px-3 py-2" role="status">
		<div class="flex items-center gap-2 text-sm text-muted-foreground">
			{#if checkingUpdates || stagingUpdates}
				<CarbonSpinner
					size="sm"
					label={stagingUpdates ? 'Staging mod updates' : 'Checking installed mods for updates'}
				/>
			{/if}
			<span class="min-w-0 flex-1">{updateStatus}</span>
		</div>
		{#if uploading && uploadProgress}
			<div class="mt-2 text-xs">
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
	</div>

	<!-- Pending updates card: the review flow is isolated above the list so
		update controls never interleave with installed rows. -->
	{#if availableUpdates.length > 0}
		<div
			class="flex flex-col gap-3 border border-border bg-card p-3 sm:flex-row sm:flex-wrap sm:items-end"
		>
			<p class="w-full font-mono text-xs font-semibold tracking-wider text-foreground uppercase">
				Compatible updates ({availableUpdates.length})
			</p>
			<div class="min-w-0">
				<label
					for="mod-update-window"
					class="mb-1 block font-mono text-xs text-muted-foreground uppercase"
				>
					Apply window
				</label>
				<select
					id="mod-update-window"
					bind:value={updateWindow}
					class="h-8 rounded-none border border-border bg-background px-2 text-xs text-foreground focus:border-primary focus:outline-none"
				>
					<option value="next">Next restart</option>
					<option value="time">At a chosen time (browser local)</option>
					<option value="idle">When the server is empty</option>
				</select>
			</div>
			{#if updateWindow === 'time'}
				<div class="min-w-0">
					<label
						for="mod-update-apply-at"
						class="mb-1 block font-mono text-xs text-muted-foreground uppercase"
					>
						Apply at
					</label>
					<input
						id="mod-update-apply-at"
						type="datetime-local"
						bind:value={applyAtLocal}
						class="h-8 rounded-none border border-border bg-background px-2 text-xs text-foreground focus:border-primary focus:outline-none"
					/>
				</div>
			{/if}
			{#if updateWindow === 'idle'}
				<div class="min-w-0">
					<label
						for="mod-update-idle"
						class="mb-1 block font-mono text-xs text-muted-foreground uppercase"
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
				disabled={stagingUpdates || currentSelectedUpdates.length === 0}
				onclick={stageSelectedUpdates}
			>
				{#if stagingUpdates}
					<CarbonSpinner size="sm" label="Staging mod updates" />
				{/if}
				Stage {currentSelectedUpdates.length} selected update{currentSelectedUpdates.length === 1
					? ''
					: 's'}
			</CarbonButton>
			{#if stagedOutcomes.length > 0}
				<ul class="w-full border-t border-border pt-2" aria-label="Staging results">
					{#each stagedOutcomes as row (row.modId)}
						<li class="flex flex-wrap items-center gap-2 py-0.5">
							<CarbonTag type={updateOutcomeTagType(row.outcome)} size="sm">
								{updateOutcomeLabel(row.outcome)}
							</CarbonTag>
							<span class="font-mono text-xs text-foreground">
								{mods.find((mod) => mod.id === row.modId)?.displayName || row.fileName}
							</span>
							{#if row.reason}
								<span class="text-xs text-muted-foreground">{row.reason}</span>
							{/if}
						</li>
					{/each}
				</ul>
			{/if}
		</div>
	{/if}

	<!-- Automatic update policy -->
	{#if canHaveMods()}
		<CarbonAccordion>
			<CarbonAccordionItem
				title="Automatic update policy"
				subtitle={policySummary}
				bind:open={policyOpen}
			>
				<div class="flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-end">
					<label class="flex items-center gap-2 text-xs text-foreground">
						<input
							type="checkbox"
							bind:checked={policyEnabled}
							class="h-3.5 w-3.5 accent-primary"
						/>
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
							onchange={() => (policySkipRunningExplicit = true)}
							class="h-3.5 w-3.5 accent-primary"
						/>
						Skip while running
					</label>
					<CarbonButton
						kind="secondary"
						size="sm"
						disabled={!policyReady || policyLoading || policySaving}
						onclick={savePolicy}
					>
						Save policy
					</CarbonButton>
					{#if policyLoading && !policyReady}
						<CarbonSpinner size="sm" label="Loading policy" description="Loading policy" />
					{:else if !policyReady}
						<CarbonButton kind="ghost" size="sm" onclick={loadPolicy}
							>Retry loading policy</CarbonButton
						>
					{/if}
					<dl
						class="grid w-full grid-cols-1 gap-1 border-t border-border pt-2 font-mono text-xs text-muted-foreground sm:grid-cols-4"
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
				</div>
			</CarbonAccordionItem>
		</CarbonAccordion>
	{/if}

	<div class="min-h-0 flex-1 overflow-auto bg-background">
		{#if !canHaveMods()}
			<div class="p-6">
				<CarbonEmptyState
					title="This server type doesn't load mods or plugins"
					description="To install mods or plugins, configure this server to use Fabric, Forge, NeoForge, Paper, or Purpur."
				/>
			</div>
		{:else if loading}
			<div class="flex items-center justify-center p-12">
				<CarbonInlineLoading description="Reading {getModsDirectory()}/ directory..." />
			</div>
		{:else if loadError}
			<!-- E1: a failed load is never rendered as the empty state. -->
			<div class="p-6">
				<CarbonErrorState
					title="Couldn't load installed mods"
					message={loadError}
					retryLabel="Retry"
					onretry={() => loadMods()}
				/>
			</div>
		{:else if mods.length === 0}
			<div class="p-6">
				<CarbonEmptyState
					title="No mods installed"
					description="Browse the repository or upload .jar files directly to get started."
					actionLabel="Browse & install"
					onaction={() => (browserDialogOpen = true)}
				/>
			</div>
		{:else if filteredMods.length === 0}
			<div class="p-8 text-center text-xs text-muted-foreground">
				No mods match your search query or filter.
			</div>
		{:else}
			<div class="installed-mods-table">
				<CarbonDataTable
					title="Installed content"
					description="{filteredMods.length} of {mods.length} in {getModsDirectory()}/"
				>
					{#snippet header()}
						<tr class="installed-mods-header">
							<th scope="col" class="px-3 py-2.5 text-xs font-semibold tracking-wider uppercase"
								>Name</th
							>
							<th scope="col" class="px-3 py-2.5 text-xs font-semibold tracking-wider uppercase"
								>Status</th
							>
							<th
								scope="col"
								class="detail px-3 py-2.5 text-xs font-semibold tracking-wider uppercase">File</th
							>
							<th
								scope="col"
								class="detail px-3 py-2.5 text-xs font-semibold tracking-wider uppercase">Size</th
							>
							<th
								scope="col"
								class="detail px-3 py-2.5 text-xs font-semibold tracking-wider uppercase"
								>Updated</th
							>
							<th scope="col" class="px-3 py-2.5 text-xs font-semibold tracking-wider uppercase"
								>Update</th
							>
							<th
								scope="col"
								class="px-3 py-2.5 text-right text-xs font-semibold tracking-wider uppercase"
								>Actions</th
							>
						</tr>
					{/snippet}
					{#each filteredMods as mod (mod.id)}
						<tr class="transition-colors hover:bg-secondary">
							<td class="px-3 py-3 align-top">
								<div class="flex min-w-0 flex-col gap-1">
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
									{#if mod.description}
										<p class="line-clamp-1 max-w-sm font-sans text-xs text-muted-foreground">
											{mod.description}
										</p>
									{/if}
								</div>
							</td>
							<td class="px-3 py-3 align-top">
								<button
									type="button"
									onclick={() => toggleMod(mod)}
									disabled={busyMods[mod.id] !== undefined}
									class="flex h-7 cursor-pointer items-center gap-1 rounded-none border px-2 font-mono text-xs transition-colors select-none disabled:cursor-not-allowed disabled:opacity-50 {mod.enabled
										? 'border-success bg-success/20 text-success-text'
										: 'border-border bg-background text-muted-foreground'}"
									title={mod.enabled ? 'Click to disable' : 'Click to enable'}
									aria-label="{mod.enabled ? 'Disable' : 'Enable'} {mod.displayName}"
								>
									{#if mod.enabled}
										<Check class="h-3 w-3" />
										<span class="text-xs uppercase">Active</span>
									{:else}
										<Ban class="h-3 w-3" />
										<span class="text-xs uppercase">Disabled</span>
									{/if}
								</button>
							</td>
							<td
								class="detail max-w-[180px] truncate px-3 py-3 align-top font-mono text-xs text-muted-foreground"
							>
								<span class="flex items-center gap-1">
									<FileText class="h-3 w-3 shrink-0" />
									<span class="truncate">{mod.fileName}</span>
								</span>
							</td>
							<td class="detail px-3 py-3 align-top font-mono text-xs text-muted-foreground">
								{formatBytes(Number(mod.fileSize))}
							</td>
							<td class="detail px-3 py-3 align-top font-mono text-xs text-muted-foreground">
								{#if mod.uploadedAt}
									{new Date(Number(mod.uploadedAt.seconds) * 1000).toLocaleDateString()}
								{:else}
									—
								{/if}
							</td>
							<td class="px-3 py-3 align-top">
								{#if mod.updatePreview}
									<div class="flex min-w-0 flex-col gap-1">
										<div class="flex flex-wrap items-center gap-2">
											<CarbonTag
												type={updateStateTagType(mod.updatePreview.state)}
												size="sm"
												title={mod.updatePreview.reason}
											>
												{mod.updatePreview.state.replace(/_/g, ' ')}
											</CarbonTag>
											{#if mod.updatePreview.state === 'available'}
												<label
													class="flex cursor-pointer items-center gap-1.5 text-xs text-foreground"
												>
													<input
														type="checkbox"
														checked={currentSelectedUpdates.includes(mod.id)}
														onchange={() => toggleUpdateSelection(mod.id)}
														class="h-3.5 w-3.5 accent-primary"
													/>
													Stage this update
												</label>
											{/if}
										</div>
										{#if mod.updatePreview.installedVersion}
											<p class="text-xs break-words text-muted-foreground">
												{mod.updatePreview.provider || 'Provider'} · Installed
												{mod.updatePreview.installedVersion}
												{#if mod.updatePreview.candidateVersion && mod.updatePreview.candidateVersionId !== mod.updatePreview.installedVersionId}
													· Candidate {mod.updatePreview.candidateVersion}
												{/if}
											</p>
										{/if}
										<p class="text-xs text-muted-foreground">{mod.updatePreview.reason}</p>
									</div>
								{:else}
									<span class="font-mono text-xs text-muted-foreground">—</span>
								{/if}
							</td>
							<td class="px-3 py-3 text-right align-top">
								<div class="inline-flex items-center border border-border bg-background">
									<button
										type="button"
										onclick={() => downloadMod(mod)}
										class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none border-r border-border text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
										title="Download {getModsDirectory() === 'plugins' ? 'plugin' : 'mod'} file"
										aria-label="Download {mod.fileName}"
									>
										<Download class="h-3.5 w-3.5" />
									</button>
									<button
										type="button"
										onclick={() => deleteMod(mod)}
										disabled={busyMods[mod.id] !== undefined}
										class="flex h-8 w-8 cursor-pointer items-center justify-center rounded-none text-destructive-text transition-colors hover:bg-destructive/20 disabled:cursor-not-allowed disabled:opacity-50"
										title="Delete {mod.displayName}"
										aria-label="Delete {mod.displayName}"
									>
										<Trash2 class="h-3.5 w-3.5" />
									</button>
								</div>
							</td>
						</tr>
					{/each}
				</CarbonDataTable>
			</div>
		{/if}
	</div>
</ServerTabPanel>

<ModBrowserDialog bind:open={browserDialogOpen} {server} onInstalled={loadMods} />
<CarbonConfirm
	bind:open={confirmOpen}
	title="Delete mod?"
	message="The file is removed from the server. This cannot be undone."
	confirmLabel="Delete Mod"
	danger
	confirming={Boolean(pendingDelete && busyMods[pendingDelete.id] === 'delete')}
	onconfirm={confirmDeleteMod}
	onclose={() => (pendingDelete = null)}
>
	{#snippet details()}
		{#if pendingDelete}{pendingDelete.displayName}{/if}
	{/snippet}
</CarbonConfirm>

<style>
	/* Container-query collapse for the installed table: File/Size/Updated hide
		under 800px, and narrow containers fall back to a card grid (<420px).
		The wrapper div lives in this component, so the anchor selector matches
		and the :global descendants survive pruning. */
	.installed-mods-table {
		container-type: inline-size;
	}
	@container (max-width: 800px) {
		.installed-mods-table :global(.detail) {
			display: none;
		}
	}
	@container (max-width: 420px) {
		.installed-mods-table :global(thead) {
			display: none;
		}
		.installed-mods-table :global(tbody),
		.installed-mods-table :global(tr) {
			display: block;
		}
		.installed-mods-table :global(tr) {
			display: grid;
			grid-template-columns: minmax(0, 1fr) auto;
			gap: 6px 10px;
			padding: 12px;
		}
		.installed-mods-table :global(td) {
			padding: 0;
			min-width: 0;
		}
		.installed-mods-table :global(td:first-child) {
			grid-column: 1 / 3;
		}
		.installed-mods-table :global(td:nth-child(2)) {
			grid-column: 1;
			grid-row: 2;
		}
		.installed-mods-table :global(td:nth-child(6)) {
			grid-column: 2;
			grid-row: 2;
		}
		.installed-mods-table :global(td:nth-child(7)) {
			grid-column: 2;
			grid-row: 3;
		}
	}
</style>
