<!--
SPDX-License-Identifier: MPL-2.0
-->

<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { resolve } from '$app/paths';
	import { toast } from 'svelte-sonner';
	import {
		AlertTriangle,
		BookOpen,
		ExternalLink,
		Link2,
		Lock,
		Map as MapIcon,
		RefreshCw,
		ShieldCheck,
		Unlink2
	} from '@lucide/svelte';
	import type { Server } from '$lib/proto/carbonpanel/v1/common_pb';
	import type { ConfigCategory } from '$lib/proto/carbonpanel/v1/config_pb';
	import type { CompatibilityReport, WorldRevision } from '$lib/proto/carbonpanel/v1/world_pb';
	import {
		ListWorldViewTargetsRequestSchema,
		WorldRefSchema,
		WorldRef_Kind,
		type WorldViewTarget
	} from '$lib/proto/carbonpanel/v1/worldview_pb';
	import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
	import { formatRpcError } from '$lib/api/rpc-error';
	import { authStore, checkPermission } from '$lib/stores/auth';
	import {
		newWorldRequestId,
		worldLibraryActions,
		worldLibraryInventory
	} from '$lib/stores/world-library';
	import type { WorldBindingView } from '$lib/features/world-library/actions';
	import {
		adoptionGateReason,
		bindingOutcomeDisposition,
		compatibilityVerdictPresentation,
		confidencePresentation,
		consistencyPresentation,
		formatBytes,
		isUnknownConfidence,
		modLoaderLabel,
		operationOutcomePresentation,
		shortId,
		transformStatusPresentation,
		verdictGatesBinding
	} from '$lib/features/world-library/presentation';
	import {
		describeServerWorld,
		sameCompatibilityTuple,
		shouldAcceptCompatibility,
		type CompatibilityTuple
	} from '$lib/features/server-world/state';
	import { encodeWorldKey } from '$lib/features/worldview/world-ref';
	import { effectiveLoader } from '$lib/utils/mod-loader';
	import ServerTabPanel from '$lib/components/server-ui/ServerTabPanel.svelte';
	import {
		CarbonButton,
		CarbonConfirm,
		CarbonEmptyState,
		CarbonInlineLoading,
		CarbonSelect,
		CarbonTag,
		CarbonTextInput
	} from '$lib/components/carbon';

	interface Props {
		server: Server;
		active?: boolean;
	}

	let { server, active = false }: Props = $props();

	let loading = $state(true);
	let error = $state<unknown>(null);
	let bindings = $state<WorldBindingView[]>([]);
	let targets = $state<WorldViewTarget[]>([]);
	let configCategories = $state<ConfigCategory[]>([]);
	let boundRevision = $state<WorldRevision | undefined>(undefined);
	let entries = $derived($worldLibraryInventory);

	let pickerOpen = $state(false);
	let selectedEntryId = $state('');
	let revisions = $state<WorldRevision[]>([]);
	let selectedRevisionId = $state('');
	let report = $state<CompatibilityReport | null>(null);
	// A report is approved only for the exact tuple it was checked against.
	let approvedTuple = $state<CompatibilityTuple | null>(null);
	let staleSelection = $state(false);
	let compatBusy = $state(false);
	let compatError = $state<unknown>(null);
	let bindLevelName = $state('world');
	let bindWindow = $state<'now' | 'next_restart' | 'idle'>('now');
	let confirmOpen = $state(false);
	let bindBusy = $state(false);
	let pendingOperation = $state<{
		operationId: string;
		outcome: string;
		serverId: string;
		revisionId: string;
		levelName: string;
		entryId?: string;
	} | null>(null);
	let resolveNote = $state('');
	let resolveBusy = $state(false);

	// Request identity, not rendered state.
	let navigationGeneration = 0;
	let compatibilityRequest = 0;

	let auth = $derived($authStore);
	let canReadWorlds = $derived(checkPermission(auth.permissions, 'worlds', 'read', server.id));
	let canSwapWorlds = $derived(checkPermission(auth.permissions, 'worlds', 'update', server.id));
	let canView = $derived(checkPermission(auth.permissions, 'world_view', 'read', server.id));
	let canReadConfig = $derived(
		checkPermission(auth.permissions, 'server_config', 'read', server.id)
	);

	const description = $derived(
		describeServerWorld({
			serverId: server.id,
			bindings,
			targets,
			configCategories,
			denied: !canReadWorlds,
			loading: loading && !error,
			error: Boolean(error)
		})
	);
	const configRows = $derived(
		configCategories.flatMap((category) =>
			(category.properties ?? [])
				.filter((property) =>
					['level', 'levelType', 'seed', 'gamemode', 'difficulty'].includes(property.key)
				)
				.map((property) => ({
					key: property.key,
					label: property.label || property.key,
					value: property.value
				}))
		)
	);
	const selectedEntry = $derived(entries.find((entry) => entry.id === selectedEntryId));
	const selectedRevision = $derived(
		revisions.find((revision) => revision.id === selectedRevisionId)
	);
	const currentTuple = $derived<CompatibilityTuple>({
		serverId: server.id,
		revisionId: selectedRevisionId,
		targetMcVersion: server.mcVersion.trim(),
		levelName: bindLevelName.trim()
	});
	let tupleMatches = $derived(sameCompatibilityTuple(approvedTuple, currentTuple));
	const unknownFindings = $derived(
		report ? report.findings.filter((finding) => isUnknownConfidence(finding.confidence)) : []
	);
	let gatedByVerdict = $derived(
		Boolean(report) && (unknownFindings.length > 0 || verdictGatesBinding(report?.overall))
	);
	const gateReason = $derived(
		report && !tupleMatches
			? 'The checked revision, level or target changed after the compatibility check. Re-run the read-only check before swapping.'
			: adoptionGateReason(report?.overall, unknownFindings.length, Boolean(report))
	);
	const canRunCompat = $derived(Boolean(selectedRevisionId) && !compatBusy);
	const canConfirmBind = $derived(
		canSwapWorlds &&
			Boolean(selectedRevisionId) &&
			Boolean(bindLevelName.trim()) &&
			Boolean(report) &&
			tupleMatches &&
			!staleSelection &&
			!gatedByVerdict &&
			!bindBusy
	);

	// Any change to the checked tuple invalidates the report immediately; a late
	// response for the old tuple is discarded on arrival as well.
	$effect(() => {
		const tuple = currentTuple;
		if (approvedTuple && !sameCompatibilityTuple(approvedTuple, tuple)) {
			report = null;
			approvedTuple = null;
			staleSelection = true;
		}
	});

	let previousServerId = $state(server.id);
	let hasLoaded = $state(false);
	let poll: ReturnType<typeof setInterval> | null = null;

	$effect(() => {
		if (server.id !== previousServerId) {
			previousServerId = server.id;
			// Invalidate every in-flight read/mutation for the previous server.
			navigationGeneration++;
			compatibilityRequest++;
			hasLoaded = false;
			// The old finally blocks skip their flags when the generation moved;
			// reset them here so the new page is never left permanently busy.
			loading = true;
			error = null;
			compatBusy = false;
			bindBusy = false;
			resolveBusy = false;
			bindings = [];
			targets = [];
			configCategories = [];
			boundRevision = undefined;
			report = null;
			approvedTuple = null;
			staleSelection = false;
			resolveNote = '';
			pendingOperation = null;
			closePicker();
		}
	});

	$effect(() => {
		if (active && canReadWorlds && !hasLoaded) {
			hasLoaded = true;
			void load();
		}
	});

	$effect(() => {
		if (active && hasLoaded && canReadWorlds) {
			poll = setInterval(() => void load(true), 15_000);
		} else if (poll) {
			clearInterval(poll);
			poll = null;
		}
		return () => {
			if (poll) {
				clearInterval(poll);
				poll = null;
			}
		};
	});

	$effect(() => {
		const revisionId = description.binding?.revisionId;
		const serverId = server.id;
		if (!revisionId) {
			boundRevision = undefined;
			return;
		}
		let cancelled = false;
		void worldLibraryActions
			.getRevision(revisionId)
			.then((outcome) => {
				if (
					!cancelled &&
					server.id === serverId &&
					description.binding?.revisionId === revisionId &&
					outcome.kind === 'revision'
				)
					boundRevision = outcome.value;
			})
			.catch(() => undefined);
		return () => {
			cancelled = true;
		};
	});

	async function loadTargets(): Promise<WorldViewTarget[]> {
		if (!canView) return [];
		const response = await rpcClient.worldView.listWorldViewTargets(
			create(ListWorldViewTargetsRequestSchema),
			silentCallOptions
		);
		return response.targets.filter((target) => target.serverId === server.id);
	}

	async function loadConfig(): Promise<ConfigCategory[]> {
		// Config is supplementary: a server_config/read denial degrades to
		// "configuration unknown" instead of hiding the whole world view.
		if (!canReadConfig) return [];
		const response = await rpcClient.config.getServerConfig(
			{ serverId: server.id },
			silentCallOptions
		);
		return response.categories;
	}

	async function load(silent = false) {
		if (!canReadWorlds) return;
		const generation = navigationGeneration;
		const serverId = server.id;
		if (!silent) loading = true;
		error = null;
		const results = await Promise.allSettled([
			worldLibraryActions.listBindings({ serverId: serverId }),
			loadTargets(),
			loadConfig(),
			worldLibraryInventory.refresh(silent)
		]);
		// Navigating to another detail page invalidates these observations.
		if (generation !== navigationGeneration || serverId !== server.id) return;
		const failed = results.some((result) => result.status === 'rejected');
		if (results[0].status === 'fulfilled' && results[0].value.kind === 'bindings')
			bindings = results[0].value.value;
		if (results[1].status === 'fulfilled') targets = results[1].value;
		if (results[2].status === 'fulfilled') configCategories = results[2].value;
		// A failed observation is reported as unknown; previously good data is not
		// silently replaced by an empty list that would look like "no world".
		if (failed && !silent) error = results.find((result) => result.status === 'rejected')?.reason;
		loading = false;
	}

	function closePicker() {
		pickerOpen = false;
		selectedEntryId = '';
		revisions = [];
		selectedRevisionId = '';
		report = null;
		approvedTuple = null;
		staleSelection = false;
		compatError = null;
		confirmOpen = false;
		compatibilityRequest++;
	}

	async function openPicker() {
		if (!canSwapWorlds) return;
		closePicker();
		bindLevelName = description.levelName || 'world';
		pickerOpen = true;
		await worldLibraryInventory.refresh();
	}

	async function selectEntry(entryId: string) {
		const generation = navigationGeneration;
		const serverId = server.id;
		selectedEntryId = entryId;
		report = null;
		approvedTuple = null;
		compatError = null;
		selectedRevisionId = '';
		revisions = [];
		compatibilityRequest++;
		if (!entryId) return;
		const outcome = await worldLibraryActions.listRevisions(entryId);
		if (
			generation !== navigationGeneration ||
			serverId !== server.id ||
			selectedEntryId !== entryId
		)
			return;
		if (outcome.kind !== 'revisions') return;
		revisions = outcome.value;
		selectedRevisionId = revisions[0]?.id ?? '';
	}

	async function runCompatibility() {
		if (!canRunCompat || !selectedRevisionId) return;
		const captured = { ...currentTuple };
		const generation = navigationGeneration;
		const request = ++compatibilityRequest;
		compatBusy = true;
		compatError = null;
		report = null;
		approvedTuple = null;
		staleSelection = false;
		try {
			const outcome = await worldLibraryActions.checkCompatibility({
				revisionId: captured.revisionId,
				serverId: captured.serverId,
				targetMcVersion: captured.targetMcVersion,
				levelName: captured.levelName
			});
			// A response for revision A must never unlock revision B, and a
			// navigation must never restore a report from the previous server.
			if (
				!shouldAcceptCompatibility({
					captured,
					current: currentTuple,
					navigationChanged: generation !== navigationGeneration,
					requestIsCurrent: request === compatibilityRequest
				})
			) {
				// A stale response is discarded; surface an explicit re-check state
				// for the current page instead of silently forgetting the check.
				if (generation === navigationGeneration) staleSelection = true;
				return;
			}
			if (outcome.kind === 'checked') {
				report = outcome.value ?? null;
				approvedTuple = captured;
			}
		} catch (cause) {
			if (generation !== navigationGeneration || request !== compatibilityRequest) return;
			compatError = cause;
		} finally {
			// A superseded or discarded check still releases the busy flag on
			// this page; only a navigation owns a different page's state.
			if (generation === navigationGeneration) compatBusy = false;
		}
	}

	function requestSwap() {
		if (!canSwapWorlds) return;
		if (!report || !sameCompatibilityTuple(approvedTuple, currentTuple)) {
			staleSelection = true;
			return;
		}
		if (!canConfirmBind) return;
		staleSelection = false;
		confirmOpen = true;
	}

	async function confirmSwap() {
		if (!canSwapWorlds) return;
		// Re-require the exact checked tuple at the moment of confirmation.
		if (!report || !sameCompatibilityTuple(approvedTuple, currentTuple)) {
			confirmOpen = false;
			staleSelection = true;
			toast.error('The checked selection changed. Re-run compatibility before swapping.');
			return;
		}
		if (!canConfirmBind) return;
		const captured = { ...currentTuple };
		const entryId = selectedEntryId || undefined;
		const generation = navigationGeneration;
		bindBusy = true;
		try {
			const outcome = await worldLibraryActions.bindWorld({
				serverId: captured.serverId,
				levelName: captured.levelName,
				revisionId: captured.revisionId,
				window: bindWindow,
				requestId: newWorldRequestId('bind'),
				entryId
			});
			// Navigating away makes this result irrelevant to the new page.
			if (generation !== navigationGeneration || outcome.kind !== 'bound') return;
			const result = outcome.value;
			const presentation = operationOutcomePresentation(result.outcome);
			const disposition = bindingOutcomeDisposition(result.outcome);
			if (disposition === 'applied') {
				toast.success(
					`${presentation.label}: ${result.binding?.levelName || captured.levelName} is bound to ${shortId(captured.revisionId)}.`
				);
				await load();
			} else {
				// Queued, running, failed, skipped, cancelled and unknown outcomes
				// all keep their reported label; no applied state is fabricated.
				pendingOperation = {
					operationId: result.operationId,
					outcome: result.outcome,
					serverId: captured.serverId,
					revisionId: captured.revisionId,
					levelName: captured.levelName,
					entryId
				};
				const message = `${presentation.label}. ${presentation.hint ?? ''}`.trim();
				if (disposition === 'uncertain' || disposition === 'failed') toast.error(message);
				else toast.info(message);
			}
			confirmOpen = false;
			closePicker();
		} catch (cause) {
			if (generation !== navigationGeneration) return;
			toast.error(formatRpcError(cause, 'Could not journal the world swap.'));
		} finally {
			if (generation === navigationGeneration) bindBusy = false;
		}
	}

	async function resolveOperation(action: 'recover' | 'rollback') {
		const operation = pendingOperation;
		if (!operation || resolveBusy) return;
		const generation = navigationGeneration;
		const entryId = operation.entryId;
		const note = resolveNote;
		resolveBusy = true;
		try {
			const outcome = await worldLibraryActions.resolveOperation({
				serverId: operation.serverId,
				operationId: operation.operationId,
				action,
				note,
				entryId
			});
			if (generation !== navigationGeneration || outcome.kind !== 'resolved') return;
			const presentation = operationOutcomePresentation(outcome.value.outcome);
			const disposition = bindingOutcomeDisposition(outcome.value.outcome);
			const message = `${presentation.label}. ${presentation.hint ?? ''}`.trim();
			if (disposition === 'applied') {
				toast.success(`Operation ${action} reported: ${message}`);
				pendingOperation = null;
				resolveNote = '';
			} else {
				// Keep the operation visible for another operator decision.
				toast.error(`Operation ${action} reported: ${message}`);
			}
			await load();
		} catch (cause) {
			if (generation !== navigationGeneration) return;
			toast.error(formatRpcError(cause, 'Could not resolve the world operation.'));
		} finally {
			if (generation === navigationGeneration) resolveBusy = false;
		}
	}

	function worldViewHref(): string | null {
		if (!canView || !description.levelName) return null;
		const ref = description.binding
			? create(WorldRefSchema, {
					kind: WorldRef_Kind.LIBRARY_BINDING,
					serverId: server.id,
					levelName: description.levelName,
					bindingId: description.binding.id
				})
			: create(WorldRefSchema, {
					kind: WorldRef_Kind.SERVER_LOCAL,
					serverId: server.id,
					levelName: description.levelName
				});
		return resolve('/worlds/[server]/[world]', { server: server.id, world: encodeWorldKey(ref) });
	}

	function libraryHref(): string {
		const revisionId = description.binding?.revisionId;
		const entry = revisionId
			? entries.find(
					(candidate) =>
						candidate.currentRevisionId === revisionId ||
						candidate.id === boundRevision?.worldEntryId
				)
			: undefined;
		return entry ? resolve('/library/[id]', { id: entry.id }) : resolve('/library');
	}
</script>

<ServerTabPanel
	title="World"
	description="The active level, its library binding and read-only compatibility checks. World bytes are never edited here."
	mode="form"
>
	{#snippet chip()}
		<CarbonTag type={description.presentation.tone} size="sm">
			{description.presentation.label}
		</CarbonTag>
	{/snippet}
	{#snippet actions()}
		<CarbonButton
			kind="secondary"
			size="sm"
			onclick={() => void load()}
			disabled={!canReadWorlds || loading}
			class="justify-center gap-2"
		>
			<RefreshCw class="h-3.5 w-3.5" />
			Refresh
		</CarbonButton>
		{#if canSwapWorlds && canReadWorlds}
			<CarbonButton
				kind="primary"
				size="sm"
				onclick={() => void openPicker()}
				class="justify-center gap-2"
			>
				<Link2 class="h-3.5 w-3.5" />
				{description.binding ? 'Swap library world' : 'Pick library world'}
			</CarbonButton>
		{/if}
	{/snippet}

	<div class="flex flex-col gap-4">
		{#if pendingOperation}
			{@const pendingPresentation = operationOutcomePresentation(pendingOperation.outcome)}
			{@const pendingDisposition = bindingOutcomeDisposition(pendingOperation.outcome)}
			<div
				class="border border-warning bg-card p-4"
				role="alert"
				aria-live="assertive"
				data-testid="server-world-operation-status"
			>
				<div class="flex flex-wrap items-start gap-3">
					<AlertTriangle class="mt-0.5 h-5 w-5 shrink-0 text-warning-text" />
					<div class="min-w-0 flex-1">
						<div class="flex flex-wrap items-center gap-2">
							<h3 class="text-sm font-semibold text-foreground">
								{pendingDisposition === 'uncertain' ? 'Uncertain operation' : 'Operation status'}
							</h3>
							<CarbonTag type={pendingPresentation.tone} size="sm">
								{pendingPresentation.label}
							</CarbonTag>
						</div>
						<p class="mt-1 text-xs text-muted-foreground">{pendingPresentation.hint}</p>
						<p class="mt-1 font-mono text-xs break-all text-foreground">
							Operation {pendingOperation.operationId} · server {pendingOperation.serverId} · level
							{pendingOperation.levelName} · revision {shortId(pendingOperation.revisionId)}
						</p>
						{#if pendingDisposition === 'uncertain'}
							<p class="mt-1 text-xs text-muted-foreground">
								Resolve from observed node state. Recovering acknowledges the applied state; rolling
								back restores the prior level family. No automatic retry is attempted.
							</p>
							<div class="mt-3 max-w-md">
								<CarbonTextInput
									label="Resolution note (optional)"
									placeholder="What did you observe on the node?"
									bind:value={resolveNote}
									disabled={resolveBusy}
								/>
							</div>
							<div class="mt-3 flex flex-wrap gap-2">
								<CarbonButton
									kind="primary"
									size="sm"
									loading={resolveBusy}
									onclick={() => void resolveOperation('recover')}
									class="justify-center"
								>
									Recover to observed state
								</CarbonButton>
								<CarbonButton
									kind="danger"
									size="sm"
									loading={resolveBusy}
									onclick={() => void resolveOperation('rollback')}
									class="justify-center"
								>
									Roll back
								</CarbonButton>
							</div>
						{:else}
							<p class="mt-1 text-xs text-muted-foreground">
								No success is claimed and nothing is retried automatically. The journal entry in the
								Activity tab is authoritative; refresh to observe the current node state.
							</p>
							<CarbonButton
								kind="secondary"
								size="sm"
								onclick={() => void load()}
								class="mt-3 justify-center"
							>
								Refresh state
							</CarbonButton>
						{/if}
					</div>
				</div>
			</div>
		{/if}

		{#if !canReadWorlds}
			<CarbonEmptyState
				title="No world access on this server"
				description="Your account needs worlds/read on this server. No binding, level name or world observation is shown."
			>
				{#snippet icon()}
					<Lock class="h-6 w-6" />
				{/snippet}
			</CarbonEmptyState>
		{:else if loading && !error}
			<div class="border border-border bg-card p-6" role="status">
				<CarbonInlineLoading description="Checking the active world and its binding…" />
			</div>
		{:else if error}
			<div class="border border-border bg-card p-4" role="alert">
				<div class="flex flex-wrap items-start gap-3">
					<AlertTriangle class="mt-0.5 h-5 w-5 shrink-0 text-destructive-text" />
					<div class="min-w-0">
						<h3 class="text-sm font-semibold text-foreground">World observations unavailable</h3>
						<p class="mt-1 text-xs text-muted-foreground">
							{formatRpcError(error, 'The binding, view or configuration could not be read.')}
							No state is inferred from this failure.
						</p>
						<CarbonButton
							kind="secondary"
							size="sm"
							onclick={() => void load()}
							class="mt-3 justify-center"
						>
							Retry
						</CarbonButton>
					</div>
				</div>
			</div>
		{:else}
			<!--
				The state label is shown once in the tab header. Keep the full
				hint here, inset like the other world cards, so it reads as
				guidance instead of an edge-aligned orphan line.
			-->
			<div class="border border-border bg-card p-3 sm:p-4">
				<p class="mono-label">World state</p>
				<p class="mt-1 text-xs text-muted-foreground">{description.presentation.hint}</p>
			</div>

			<!-- Active world / level -->
			<div class="grid gap-4 sm:grid-cols-2 [&>*]:min-w-0">
				<div class="border border-border bg-card p-4">
					<p class="mono-label">Active world / level</p>
					<p class="mt-1 text-sm font-semibold break-all text-foreground">
						{description.levelName || 'NOT REPORTED'}
					</p>
					<p class="mt-1 text-xs text-muted-foreground">
						{#if description.binding}
							Tracked by a World Library binding on this level.
						{:else if description.management === 'unmanaged'}
							Observed on disk; not managed by the World Library.
						{:else if description.management === 'unbound'}
							Configured level; no active world was observed.
						{:else}
							No world was reported by the available observations.
						{/if}
					</p>
				</div>
				<div class="border border-border bg-card p-4">
					<p class="mono-label">Management</p>
					<div class="mt-2 flex flex-wrap items-center gap-2">
						{#if description.target}
							<CarbonTag type="blue" size="sm">WORLD VIEW OBSERVED</CarbonTag>
						{/if}
					</div>
					<p class="mt-2 text-xs text-muted-foreground">
						Game version {server.mcVersion || '—'} · {modLoaderLabel(
							effectiveLoader(server.modLoader, server.modpackLoader)
						)}
					</p>
				</div>
			</div>

			<!-- Binding + revision -->
			{#if description.binding}
				<div class="border border-border bg-card p-4">
					<div class="flex flex-wrap items-center gap-2">
						<Link2 class="h-4 w-4 text-foreground" />
						<h3 class="mono-label-strong">Binding & revision</h3>
					</div>
					<div class="mt-3 grid gap-3 sm:grid-cols-2 [&>*]:min-w-0">
						<div>
							<p class="mono-label">Binding</p>
							<p class="mt-1 font-mono text-xs break-all text-foreground">
								{shortId(description.binding.id)}
							</p>
						</div>
						<div>
							<p class="mono-label">Revision</p>
							<p class="mt-1 font-mono text-xs break-all text-foreground">
								{shortId(description.binding.revisionId)}
							</p>
						</div>
						<div>
							<p class="mono-label">Node / generation</p>
							<p class="mt-1 font-mono text-xs break-all text-foreground">
								{description.binding.nodeId || '—'} / {description.binding.generation}
							</p>
						</div>
						<div>
							<p class="mono-label">Consistency</p>
							<div class="mt-1">
								<CarbonTag
									type={consistencyPresentation(description.binding.consistency).tone}
									size="sm"
								>
									{consistencyPresentation(description.binding.consistency).label}
								</CarbonTag>
							</div>
						</div>
					</div>
					{#if boundRevision}
						<div class="mt-3 border-t border-border pt-3">
							<p class="mono-label">Revision details</p>
							<p class="mt-1 text-xs text-foreground">
								Version {boundRevision.mcVersion || '—'} · {modLoaderLabel(boundRevision.modLoader)}
								·
								{formatBytes(boundRevision.sizeBytes)} · {boundRevision.entryCount} entries
							</p>
							{#if boundRevision.provenance}
								<p class="mt-1 text-xs break-all text-muted-foreground">
									Provenance: {boundRevision.provenance}
								</p>
							{/if}
						</div>
					{/if}
				</div>
			{:else}
				<div class="border border-border bg-card p-4">
					<div class="flex flex-wrap items-center gap-2">
						<Unlink2 class="h-4 w-4 text-foreground" />
						<h3 class="mono-label-strong">No active binding</h3>
					</div>
					<p class="mt-2 text-xs text-muted-foreground">
						{description.presentation.hint}
						Picking a compatible library world journals a stop-based swap; existing level bytes are preserved
						and never overwritten.
					</p>
				</div>
			{/if}

			<!-- Relevant configuration -->
			{#if configRows.length > 0}
				<div class="border border-border bg-card p-4">
					<h3 class="mono-label-strong">Relevant configuration</h3>
					<dl class="mt-3 grid gap-3 sm:grid-cols-2 [&>*]:min-w-0">
						{#each configRows as row (row.key)}
							<div class="min-w-0">
								<dt class="mono-label">{row.label}</dt>
								<dd class="mt-1 font-mono text-xs break-all text-foreground">{row.value}</dd>
							</div>
						{/each}
					</dl>
				</div>
			{/if}

			<!-- Deep links -->
			<div class="flex flex-wrap gap-2">
				{#if worldViewHref()}
					<CarbonButton
						kind="tertiary"
						size="sm"
						href={worldViewHref()!}
						class="justify-center gap-2"
					>
						<MapIcon class="h-3.5 w-3.5" />
						Open World View
						<ExternalLink class="h-3 w-3" />
					</CarbonButton>
				{:else}
					<span class="text-xs text-muted-foreground">
						{#if !canView}
							World View needs world_view/read on this server.
						{:else}
							World View is unavailable until a level is known.
						{/if}
					</span>
				{/if}
				<CarbonButton kind="tertiary" size="sm" href={libraryHref()} class="justify-center gap-2">
					<BookOpen class="h-3.5 w-3.5" />
					Open World Library
					<ExternalLink class="h-3 w-3" />
				</CarbonButton>
			</div>

			<!-- Pick / swap a compatible library world -->
			{#if pickerOpen && canSwapWorlds}
				<div class="border border-border bg-card p-4" aria-labelledby="server-world-picker-heading">
					<div class="flex flex-wrap items-center justify-between gap-2">
						<h3 id="server-world-picker-heading" class="mono-label-strong">
							{description.binding ? 'Swap to a library revision' : 'Adopt a library world'}
						</h3>
						<CarbonButton kind="tertiary" size="sm" onclick={closePicker} class="justify-center">
							Close
						</CarbonButton>
					</div>
					<p class="mt-2 text-xs text-muted-foreground">
						Compatibility is a read-only check. A compatible verdict with no unknown findings is the
						only state that permits the swap. A running server is stopped and quiesced for a now
						swap; library revisions are immutable and are never overwritten.
					</p>

					{#if staleSelection}
						<div class="mt-3 border border-warning bg-card p-3" role="status">
							<p class="text-xs text-foreground">
								The checked revision, level or target changed after the compatibility check. The old
								report was discarded; re-run the read-only check for this selection before swapping.
							</p>
							<CarbonButton
								kind="secondary"
								size="sm"
								class="mt-2 justify-center"
								disabled={!canRunCompat}
								onclick={() => void runCompatibility()}
							>
								Re-check compatibility
							</CarbonButton>
						</div>
					{/if}

					<div class="mt-4 grid gap-4 lg:grid-cols-2 [&>*]:min-w-0">
						<div>
							<CarbonSelect
								label="Library world"
								bind:value={selectedEntryId}
								onchange={(event) => void selectEntry(event.currentTarget.value)}
							>
								<option value="">Select a world…</option>
								{#each entries as entry (entry.id)}
									<option value={entry.id}>
										{entry.name || shortId(entry.id)} · {entry.edition || 'edition unknown'}
									</option>
								{/each}
							</CarbonSelect>
						</div>
						<div>
							<CarbonSelect
								label="Revision"
								bind:value={selectedRevisionId}
								disabled={revisions.length === 0}
							>
								<option value="">Select a revision…</option>
								{#each revisions as revision (revision.id)}
									<option value={revision.id}>
										{shortId(revision.id)} · {revision.mcVersion || 'version unknown'} · {modLoaderLabel(
											revision.modLoader
										)}
									</option>
								{/each}
							</CarbonSelect>
						</div>
					</div>

					{#if selectedRevision}
						<div class="mt-3 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
							<ShieldCheck class="h-3.5 w-3.5" />
							<span
								>Selected: {shortId(selectedRevision.id)} · {formatBytes(
									selectedRevision.sizeBytes
								)}</span
							>
						</div>
					{/if}

					<div class="mt-4 flex flex-wrap items-end gap-3">
						<div class="min-w-0 flex-1">
							<CarbonTextInput
								label="Target level name"
								bind:value={bindLevelName}
								placeholder="world"
								disabled={bindBusy}
							/>
						</div>
						<div class="min-w-0">
							<CarbonSelect label="Change window" bind:value={bindWindow}>
								<option value="now">Now (stops the server)</option>
								<option value="next_restart">Next restart</option>
								<option value="idle">When idle</option>
							</CarbonSelect>
						</div>
						<CarbonButton
							kind="secondary"
							size="sm"
							loading={compatBusy}
							disabled={!canRunCompat}
							onclick={() => void runCompatibility()}
							class="justify-center"
						>
							Check compatibility
						</CarbonButton>
					</div>

					{#if compatError}
						<div class="mt-3 border border-destructive bg-card p-3" role="alert">
							<p class="text-xs text-foreground">
								{formatRpcError(compatError, 'The compatibility check could not be read.')}
								Nothing was changed.
							</p>
						</div>
					{/if}

					{#if report}
						<div class="mt-4 border border-border bg-card p-4">
							<div class="flex flex-wrap items-center gap-2">
								<h4 class="mono-label-strong">Compatibility verdict</h4>
								<CarbonTag type={compatibilityVerdictPresentation(report.overall).tone} size="sm">
									{compatibilityVerdictPresentation(report.overall).label}
								</CarbonTag>
							</div>
							{#if report.findings.length > 0}
								<ul class="mt-3 space-y-2">
									{#each report.findings as finding, index (index)}
										<li class="flex flex-wrap items-start gap-2 text-xs">
											<CarbonTag type={confidencePresentation(finding.confidence).tone} size="sm">
												{confidencePresentation(finding.confidence).label}
											</CarbonTag>
											<span class="text-foreground">
												{finding.subject}: {finding.value || '—'}
											</span>
											{#if finding.reason}
												<span class="text-muted-foreground">— {finding.reason}</span>
											{/if}
										</li>
									{/each}
								</ul>
							{/if}
							{#if report.transforms.length > 0}
								<ul class="mt-3 space-y-2">
									{#each report.transforms as transform, index (index)}
										<li class="flex flex-wrap items-start gap-2 text-xs">
											<CarbonTag
												type={transformStatusPresentation(transform.status).tone}
												size="sm"
											>
												{transformStatusPresentation(transform.status).label}
											</CarbonTag>
											<span class="text-foreground">{transform.name}</span>
											{#if transform.reason}
												<span class="text-muted-foreground">— {transform.reason}</span>
											{/if}
										</li>
									{/each}
								</ul>
							{/if}
							{#if Object.keys(report.unsupported).length > 0}
								<p class="mt-3 text-xs text-foreground">
									Unsupported: {Object.entries(report.unsupported)
										.map(([key, reason]) => `${key} (${reason})`)
										.join(', ')}
								</p>
							{/if}
							{#if gateReason}
								<p class="mt-3 text-xs text-muted-foreground" role="status">{gateReason}</p>
							{/if}
						</div>
					{/if}

					<div class="mt-4 flex flex-wrap items-center gap-3">
						<CarbonButton
							kind="primary"
							size="sm"
							disabled={staleSelection ? !canRunCompat : !canConfirmBind}
							onclick={staleSelection ? () => void runCompatibility() : requestSwap}
							class="justify-center"
						>
							{staleSelection
								? 'Re-check compatibility'
								: selectedEntry
									? 'Review & swap'
									: 'Select a world to continue'}
						</CarbonButton>
						{#if !report}
							<span class="text-xs text-muted-foreground">
								Compatibility must be checked before swapping.
							</span>
						{:else if !tupleMatches}
							<span class="text-xs text-destructive-text">{gateReason}</span>
						{:else if gatedByVerdict}
							<span class="text-xs text-destructive-text">{gateReason}</span>
						{/if}
					</div>
				</div>
			{/if}
		{/if}
	</div>
</ServerTabPanel>

<CarbonConfirm
	bind:open={confirmOpen}
	title="Swap the active level?"
	message="This journals a stop-based swap to the selected immutable library revision. Review the target before continuing."
	confirmLabel="Confirm swap"
	cancelLabel="Cancel"
	danger={true}
	confirming={bindBusy}
	onconfirm={() => void confirmSwap()}
>
	{#snippet details()}
		<div class="space-y-1">
			<p>Server: {server.name} ({server.id})</p>
			<p>Level: {bindLevelName.trim() || '—'}</p>
			<p>
				From: {description.binding ? shortId(description.binding.revisionId) : 'no library binding'}
			</p>
			<p>To: {shortId(selectedRevisionId)} ({selectedRevision?.mcVersion || 'version unknown'})</p>
			<p>Window: {bindWindow}</p>
			<p class="pt-1">
				Library revisions are immutable and are never overwritten. The server receives its own
				writable clone; existing level bytes are preserved. An uncertain outcome is journaled for
				operator resolution, never retried automatically.
			</p>
		</div>
	{/snippet}
</CarbonConfirm>
