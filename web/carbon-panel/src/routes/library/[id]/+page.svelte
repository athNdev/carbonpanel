<!--
SPDX-License-Identifier: MPL-2.0
-->

<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { toast } from 'svelte-sonner';
	import {
		newWorldRequestId,
		worldLibraryActions,
		worldLibraryInventory
	} from '$lib/stores/world-library';
	import { formatRpcError } from '$lib/api/rpc-error';
	import type {
		CompatibilityReport,
		WorldImportJob,
		WorldRevision
	} from '$lib/proto/carbonpanel/v1/world_pb';
	import type { WorldBindingView } from '$lib/features/world-library/actions';
	import {
		bindingGateReason,
		compatibilityVerdictPresentation,
		confidencePresentation,
		consistencyPresentation,
		formatBytes,
		formatTimestamp,
		isUnknownConfidence,
		verdictGatesBinding,
		modLoaderLabel,
		operationOutcomePresentation,
		shortId,
		transformStatusPresentation,
		importPhasePresentation
	} from '$lib/features/world-library/presentation';
	import {
		CarbonBreadcrumbs,
		CarbonButton,
		CarbonConfirm,
		CarbonEmptyState,
		CarbonErrorState,
		CarbonInlineLoading,
		CarbonPageHeader,
		CarbonStructuredList,
		CarbonTabs,
		CarbonTag,
		CarbonTextInput
	} from '$lib/components/carbon';
	import CarbonInactiveTabPanels from '$lib/components/carbon/CarbonInactiveTabPanels.svelte';
	import { AlertTriangle, BookOpen, Link2, RefreshCw, ShieldCheck, Unlink2 } from '@lucide/svelte';

	const entryId = $derived(page.params.id ?? '');
	const inventoryState = worldLibraryInventory.state;
	let entry = $derived($worldLibraryInventory.find((candidate) => candidate.id === entryId));
	let detailError = $state<unknown>(null);

	let revisions = $state<WorldRevision[]>([]);
	let bindings = $state<WorldBindingView[]>([]);
	let jobs = $state<WorldImportJob[]>([]);
	let selectedRevisionId = $state('');

	let compatServerId = $state('');
	let compatTargetVersion = $state('');
	let compatBusy = $state(false);
	let compatError = $state<unknown>(null);
	let report = $state<CompatibilityReport | null>(null);

	let bindOpen = $state(false);
	let bindServerId = $state('');
	let bindLevelName = $state('world');
	let bindWindow = $state<'now' | 'next_restart' | 'idle'>('now');
	let bindBusy = $state(false);

	let pendingUnbind = $state<WorldBindingView | null>(null);
	let unbindOpen = $state(false);
	let unbindBusy = $state(false);

	let pendingOperation = $state<{
		operationId: string;
		outcome: string;
		serverId: string;
	} | null>(null);
	let resolveNote = $state('');
	let resolveBusy = $state(false);

	const tabs = [
		{ id: 'revisions', label: 'Revisions' },
		{ id: 'bindings', label: 'Bindings' },
		{ id: 'compatibility', label: 'Compatibility' },
		{ id: 'jobs', label: 'Import jobs' }
	];
	let selectedTab = $state('revisions');

	let loadingDetail = $derived(!$inventoryState.hydrated && !entry && !detailError);
	let unknownFindings = $derived(
		report ? report.findings.filter((finding) => isUnknownConfidence(finding.confidence)) : []
	);
	// Verdicts are not confidence values: compatible is the only verdict that
	// permits binding. A missing verdict or any unknown finding still fails
	// closed, and a blocked/requires_decision verdict stays gated.
	let gateReason = $derived(
		report
			? bindingGateReason(report.overall, unknownFindings.length)
			: 'Unknown findings gate binding. The check is read-only; resolve the missing evidence before continuing.'
	);
	let gatedByVerdict = $derived(
		Boolean(report) && (unknownFindings.length > 0 || verdictGatesBinding(report?.overall))
	);
	let canBind = $derived(
		Boolean(selectedRevisionId) &&
			Boolean(compatServerId.trim()) &&
			Boolean(bindLevelName.trim()) &&
			Boolean(report) &&
			!gatedByVerdict
	);

	onMount(() => {
		void loadDetail();
	});

	async function loadRevisions() {
		const outcome = await worldLibraryActions.listRevisions(entryId);
		if (outcome.kind !== 'revisions') return;
		revisions = outcome.value;
		if (!selectedRevisionId && revisions.length > 0) {
			selectedRevisionId = revisions[0].id;
		}
	}

	async function loadBindings() {
		const outcome = await worldLibraryActions.listBindings({});
		if (outcome.kind !== 'bindings') return;
		bindings = outcome.value.filter((binding) =>
			revisions.some((revision) => revision.id === binding.revisionId)
		);
	}

	async function loadJobs() {
		const outcome = await worldLibraryActions.listImportJobs(entryId);
		if (outcome.kind === 'jobs') jobs = outcome.value;
	}

	async function loadDetail() {
		detailError = null;
		const results = await Promise.allSettled([
			worldLibraryInventory.load(entryId),
			loadRevisions(),
			loadBindings(),
			loadJobs()
		]);
		const detailResult = results[0];
		if (detailResult.status === 'rejected') detailError = detailResult.reason;
		// Bindings are filtered by the revision list; recompute once both landed.
		await loadBindings();
	}

	async function runCompatibility() {
		if (!selectedRevisionId) return;
		compatBusy = true;
		compatError = null;
		try {
			const outcome = await worldLibraryActions.checkCompatibility({
				revisionId: selectedRevisionId,
				serverId: compatServerId.trim(),
				targetMcVersion: compatTargetVersion.trim()
			});
			if (outcome.kind === 'checked') report = outcome.value ?? null;
		} catch (error) {
			compatError = error;
			report = null;
		} finally {
			compatBusy = false;
		}
	}

	function openBind() {
		if (!selectedRevisionId) {
			toast.error('Select a revision first.');
			return;
		}
		bindServerId = compatServerId;
		bindOpen = true;
	}

	async function confirmBind() {
		bindBusy = true;
		try {
			const outcome = await worldLibraryActions.bindWorld({
				serverId: bindServerId.trim(),
				levelName: bindLevelName.trim(),
				revisionId: selectedRevisionId,
				window: bindWindow,
				requestId: newWorldRequestId('bind'),
				entryId
			});
			if (outcome.kind !== 'bound') return;
			const result = outcome.value;
			const uncertain = operationOutcomePresentation(result.outcome).label === 'OUTCOME UNKNOWN';
			if (uncertain) {
				// No silent retry: the operator must resolve from real node state.
				pendingOperation = {
					operationId: result.operationId,
					outcome: result.outcome,
					serverId: bindServerId.trim()
				};
				toast.error('Bind outcome is unknown. Resolve it before trying again.');
			} else {
				toast.success(`Bound ${result.binding?.levelName || bindLevelName} to the revision.`);
				await loadBindings();
			}
			bindOpen = false;
		} catch (error) {
			toast.error(formatRpcError(error, 'Could not journal the bind operation.'));
		} finally {
			bindBusy = false;
		}
	}

	async function confirmUnbind() {
		const target = pendingUnbind;
		if (!target) return;
		unbindBusy = true;
		try {
			const outcome = await worldLibraryActions.unbindWorld({
				serverId: target.serverId,
				levelName: target.levelName,
				requestId: newWorldRequestId('unbind'),
				entryId
			});
			if (outcome.kind !== 'unbound') return;
			if (outcome.value.removed) toast.success('Binding removed. World bytes were untouched.');
			else toast.info('There was no active binding to remove.');
			pendingUnbind = null;
			unbindOpen = false;
			await loadBindings();
		} catch (error) {
			toast.error(formatRpcError(error, 'Could not remove the binding.'));
		} finally {
			unbindBusy = false;
		}
	}

	async function resolveOperation(action: 'recover' | 'rollback') {
		const operation = pendingOperation;
		if (!operation || resolveBusy) return;
		resolveBusy = true;
		try {
			const outcome = await worldLibraryActions.resolveOperation({
				serverId: operation.serverId,
				operationId: operation.operationId,
				action,
				note: resolveNote,
				entryId
			});
			if (outcome.kind !== 'resolved') return;
			toast.success(`Operation ${action} reported: ${outcome.value.outcome || 'done'}.`);
			pendingOperation = null;
			resolveNote = '';
			await loadBindings();
			await loadRevisions();
		} catch (error) {
			toast.error(formatRpcError(error, 'Could not resolve the operation.'));
		} finally {
			resolveBusy = false;
		}
	}
</script>

<div class="space-y-6 font-sans text-foreground" data-testid="showcase-library-detail-content">
	<CarbonPageHeader
		title={entry?.name || shortId(entryId)}
		description="Revision history, provenance, bindings, and read-only compatibility findings."
	>
		{#snippet tag()}
			{#if entry}
				<CarbonTag type={entry.edition === 'bedrock' ? 'teal' : 'blue'} size="sm">
					{entry.edition ? entry.edition.toUpperCase() : 'UNSPECIFIED'}
				</CarbonTag>
			{/if}
		{/snippet}
		{#snippet actions()}
			<CarbonButton
				kind="secondary"
				size="sm"
				onclick={() => void loadDetail()}
				class="justify-center gap-2"
			>
				<RefreshCw class="h-4 w-4" />
				Refresh
			</CarbonButton>
			<CarbonButton
				kind="primary"
				size="sm"
				disabled={!canBind}
				onclick={openBind}
				class="justify-center gap-2"
			>
				<Link2 class="h-4 w-4" />
				Bind revision
			</CarbonButton>
		{/snippet}
	</CarbonPageHeader>

	<CarbonBreadcrumbs
		class="focus-ring"
		items={[
			{ href: resolve('/library'), label: 'World Library' },
			{ label: entry?.name || shortId(entryId) }
		]}
	/>

	{#if loadingDetail}
		<CarbonInlineLoading description="Loading world entry…" />
	{:else if detailError}
		<CarbonErrorState
			title="Could not load this world entry"
			message={formatRpcError(detailError, 'The entry may have been removed or access was denied.')}
			onretry={() => void loadDetail()}
		/>
	{:else if !entry}
		<CarbonEmptyState
			title="World entry not found"
			description="This library entry is not available. It may have been removed."
		>
			{#snippet icon()}
				<BookOpen class="h-6 w-6" />
			{/snippet}
		</CarbonEmptyState>
	{:else}
		<div class="grid gap-4 lg:grid-cols-3 [&>*]:min-w-0">
			<div class="border border-border bg-card p-4">
				<p class="mono-label">Entry ID</p>
				<p class="mt-1 font-mono text-xs break-all text-foreground">{entry.id}</p>
			</div>
			<div class="border border-border bg-card p-4">
				<p class="mono-label">Family</p>
				<p class="mt-1 text-sm text-foreground">{entry.family || '—'}</p>
			</div>
			<div class="border border-border bg-card p-4">
				<p class="mono-label">Created</p>
				<p class="mt-1 font-mono text-xs text-foreground">{formatTimestamp(entry.createdAt)}</p>
			</div>
		</div>

		{#if pendingOperation}
			<div class="border border-warning bg-card p-4" role="alert" aria-live="assertive">
				<div class="flex flex-wrap items-start gap-3">
					<AlertTriangle class="mt-0.5 h-5 w-5 shrink-0 text-warning-text" />
					<div class="min-w-0 flex-1">
						<div class="flex flex-wrap items-center gap-2">
							<h3 class="text-sm font-semibold text-foreground">Uncertain operation</h3>
							<CarbonTag type="yellow" size="sm">
								{operationOutcomePresentation(pendingOperation.outcome).label}
							</CarbonTag>
						</div>
						<p class="mt-1 text-xs text-muted-foreground">
							{operationOutcomePresentation(pendingOperation.outcome).hint}
						</p>
						<p class="mt-1 font-mono text-xs break-all text-foreground">
							Operation {pendingOperation.operationId} · server {pendingOperation.serverId}
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
					</div>
				</div>
			</div>
		{/if}

		<div class="border border-border bg-card">
			<CarbonTabs {tabs} bind:selectedTab idPrefix="world" />
			<CarbonInactiveTabPanels {tabs} {selectedTab} idPrefix="world" />
			<div class="p-4">
				{#if selectedTab === 'revisions'}
					<div
						id="world-panel-revisions"
						role="tabpanel"
						aria-labelledby="world-tab-revisions"
						class="focus-ring"
						tabindex="0"
					>
						{#if revisions.length === 0}
							<CarbonEmptyState
								title="No revisions"
								description="This entry has no immutable revisions yet."
							/>
						{:else}
							<div class="overflow-x-auto">
								<table class="w-full border-collapse text-left text-sm">
									<thead class="border-b border-border text-xs text-muted-foreground uppercase">
										<tr>
											<th scope="col" class="px-3 py-2">Revision</th>
											<th scope="col" class="px-3 py-2">Version / loader</th>
											<th scope="col" class="px-3 py-2">Size / entries</th>
											<th scope="col" class="px-3 py-2">Provenance</th>
											<th scope="col" class="px-3 py-2">State</th>
											<th scope="col" class="px-3 py-2">Action</th>
										</tr>
									</thead>
									<tbody class="divide-y divide-border">
										{#each revisions as revision (revision.id)}
											<tr class="hover:bg-accent">
												<td class="px-3 py-2.5">
													<span class="font-mono text-xs break-all text-foreground">
														{shortId(revision.id)}
													</span>
													<span class="block font-mono text-xs text-muted-foreground">
														SHA-256 {shortId(revision.sha256)}
													</span>
												</td>
												<td class="px-3 py-2.5 font-mono text-xs text-foreground">
													{revision.mcVersion || '—'} · {modLoaderLabel(revision.modLoader)}
													<span class="block text-muted-foreground">
														data {revision.dataVersion || '—'}
													</span>
												</td>
												<td class="px-3 py-2.5 font-mono text-xs text-foreground">
													{formatBytes(revision.sizeBytes)}<span
														class="block text-muted-foreground"
													>
														{revision.entryCount} entries
													</span>
												</td>
												<td class="px-3 py-2.5 text-xs text-muted-foreground">
													<span class="block break-all">{revision.provenance || '—'}</span>
													<span class="block"
														>{revision.licenseAttestation || 'No attestation'}</span
													>
												</td>
												<td class="px-3 py-2.5">
													<CarbonTag type={revision.bound ? 'yellow' : 'gray'} size="sm">
														{revision.bound ? 'BOUND' : 'UNBOUND'}
													</CarbonTag>
												</td>
												<td class="px-3 py-2.5">
													<CarbonButton
														kind={selectedRevisionId === revision.id ? 'primary' : 'tertiary'}
														size="sm"
														onclick={() => {
															selectedRevisionId = revision.id;
															selectedTab = 'compatibility';
														}}
														class="justify-center"
													>
														{selectedRevisionId === revision.id ? 'Selected' : 'Check'}
													</CarbonButton>
												</td>
											</tr>
										{/each}
									</tbody>
								</table>
							</div>
						{/if}
					</div>
				{:else if selectedTab === 'bindings'}
					<div
						id="world-panel-bindings"
						role="tabpanel"
						aria-labelledby="world-tab-bindings"
						class="focus-ring"
						tabindex="0"
					>
						{#if bindings.length === 0}
							<CarbonEmptyState
								title="No active bindings"
								description="Binding stops the server, swaps in the revision, and gives the server its own writable copy."
							/>
						{:else}
							<div class="overflow-x-auto">
								<table class="w-full border-collapse text-left text-sm">
									<thead class="border-b border-border text-xs text-muted-foreground uppercase">
										<tr>
											<th scope="col" class="px-3 py-2">Server / level</th>
											<th scope="col" class="px-3 py-2">Revision</th>
											<th scope="col" class="px-3 py-2">Node / generation</th>
											<th scope="col" class="px-3 py-2">Consistency</th>
											<th scope="col" class="px-3 py-2">Action</th>
										</tr>
									</thead>
									<tbody class="divide-y divide-border">
										{#each bindings as binding (binding.id)}
											<tr class="hover:bg-accent">
												<td class="px-3 py-2.5 text-xs text-foreground">
													<span class="font-mono break-all">{binding.serverId}</span>
													<span class="block text-muted-foreground">{binding.levelName}</span>
												</td>
												<td class="px-3 py-2.5 font-mono text-xs break-all text-foreground">
													{shortId(binding.revisionId)}
												</td>
												<td class="px-3 py-2.5 font-mono text-xs text-muted-foreground">
													{shortId(binding.nodeId)} / {binding.generation}
												</td>
												<td class="px-3 py-2.5">
													<CarbonTag
														type={consistencyPresentation(binding.consistency).tone}
														size="sm"
													>
														{consistencyPresentation(binding.consistency).label}
													</CarbonTag>
												</td>
												<td class="px-3 py-2.5">
													<CarbonButton
														kind="danger"
														size="sm"
														disabled={unbindBusy}
														onclick={() => {
															pendingUnbind = binding;
															unbindOpen = true;
														}}
														class="justify-center gap-2"
													>
														<Unlink2 class="h-3.5 w-3.5" />
														Unbind
													</CarbonButton>
												</td>
											</tr>
										{/each}
									</tbody>
								</table>
							</div>
						{/if}
					</div>
				{:else if selectedTab === 'compatibility'}
					<div
						id="world-panel-compatibility"
						role="tabpanel"
						aria-labelledby="world-tab-compatibility"
						class="focus-ring space-y-4"
						tabindex="0"
					>
						<div class="flex flex-wrap items-end gap-3">
							<div class="min-w-48 flex-1">
								<CarbonTextInput
									label="Target server ID"
									placeholder="Server to bind this revision to"
									bind:value={compatServerId}
								/>
							</div>
							<div class="w-40">
								<CarbonTextInput
									label="Target Minecraft version"
									placeholder="e.g. 1.21.1"
									bind:value={compatTargetVersion}
								/>
							</div>
							<CarbonButton
								kind="secondary"
								size="sm"
								loading={compatBusy}
								disabled={!selectedRevisionId || !compatServerId.trim()}
								onclick={() => void runCompatibility()}
								class="justify-center gap-2"
							>
								<ShieldCheck class="h-4 w-4" />
								Check compatibility
							</CarbonButton>
						</div>
						<p class="font-mono text-xs text-muted-foreground">
							Revision {shortId(selectedRevisionId)}
						</p>

						{#if compatError}
							<div
								role="alert"
								class="border border-destructive bg-card p-3 text-xs text-destructive-text"
							>
								{formatRpcError(compatError, 'The compatibility check failed.')}
							</div>
						{/if}

						{#if report}
							<div class="border border-border bg-background p-4" aria-live="polite">
								<div class="flex flex-wrap items-center gap-2">
									<span class="mono-label">Overall verdict</span>
									<CarbonTag type={compatibilityVerdictPresentation(report.overall).tone} size="sm">
										{compatibilityVerdictPresentation(report.overall).label}
									</CarbonTag>
									<span class="text-xs text-muted-foreground">
										target {report.targetMcVersion || '—'} · server {report.serverId || '—'}
									</span>
								</div>
								{#if gateReason}
									<p class="mt-3 flex items-start gap-2 text-xs text-warning-text" role="status">
										<AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />
										{gateReason}
									</p>
								{/if}
							</div>

							<div class="space-y-2">
								<h3 class="mono-label-strong">Findings</h3>
								{#if report.findings.length === 0}
									<p class="text-xs text-muted-foreground">No findings were reported.</p>
								{:else}
									<div class="overflow-x-auto">
										<table class="w-full border-collapse text-left text-sm">
											<thead class="border-b border-border text-xs text-muted-foreground uppercase">
												<tr>
													<th scope="col" class="px-3 py-2">Subject</th>
													<th scope="col" class="px-3 py-2">Confidence</th>
													<th scope="col" class="px-3 py-2">Value</th>
													<th scope="col" class="px-3 py-2">Reason</th>
												</tr>
											</thead>
											<tbody class="divide-y divide-border">
												{#each report.findings as finding, index (index)}
													<tr>
														<td class="px-3 py-2 font-mono text-xs text-foreground">
															{finding.subject}
														</td>
														<td class="px-3 py-2">
															<CarbonTag
																type={confidencePresentation(finding.confidence).tone}
																size="sm"
															>
																{confidencePresentation(finding.confidence).label}
															</CarbonTag>
														</td>
														<td class="px-3 py-2 font-mono text-xs break-all text-foreground">
															{finding.value || '—'}
														</td>
														<td class="px-3 py-2 text-xs text-muted-foreground">
															{finding.reason ||
																confidencePresentation(finding.confidence).hint ||
																'—'}
															{#if finding.observation}
																<span class="block font-mono">
																	source {finding.observation.source || '—'} · applicability {finding
																		.observation.applicability || '—'}
																</span>
															{/if}
														</td>
													</tr>
												{/each}
											</tbody>
										</table>
									</div>
								{/if}
							</div>

							<div class="space-y-2">
								<h3 class="mono-label-strong">Dry-run transforms</h3>
								{#if report.transforms.length === 0}
									<p class="text-xs text-muted-foreground">No transforms were proposed.</p>
								{:else}
									<CarbonStructuredList>
										{#snippet header()}
											<span class="col-span-3">Transform / status</span>
											<span>Consequences</span>
										{/snippet}
										{#each report.transforms as transform, index (index)}
											<div class="grid grid-cols-1 gap-2 p-3 text-xs md:grid-cols-4">
												<div class="md:col-span-3">
													<span class="text-foreground">{transform.name}</span>
													<CarbonTag
														type={transformStatusPresentation(transform.status).tone}
														size="sm"
													>
														{transformStatusPresentation(transform.status).label}
													</CarbonTag>
												</div>
												<div class="text-muted-foreground">
													<span>{transform.reversibility || 'Reversibility —'}</span>
													<span class="block">{transform.downtime || 'Downtime —'}</span>
													<span class="block break-all">{transform.reason || ''}</span>
												</div>
											</div>
										{/each}
									</CarbonStructuredList>
								{/if}
							</div>

							{#if Object.keys(report.unsupported).length > 0}
								<div class="border border-border bg-background p-4" role="status">
									<h3 class="mono-label-strong">Unsupported actions</h3>
									<dl class="mt-2 space-y-2 text-xs">
										{#each Object.entries(report.unsupported) as [action, reason] (action)}
											<div>
												<dt class="font-mono text-foreground">{action}</dt>
												<dd class="break-all text-muted-foreground">{reason}</dd>
											</div>
										{/each}
									</dl>
								</div>
							{/if}
						{:else}
							<CarbonEmptyState
								title="No compatibility report yet"
								description="Read-only check. Findings are detected, inferred, or unknown; unknown findings block binding."
							>
								{#snippet icon()}
									<ShieldCheck class="h-6 w-6" />
								{/snippet}
							</CarbonEmptyState>
						{/if}
					</div>
				{:else}
					<div
						id="world-panel-jobs"
						role="tabpanel"
						aria-labelledby="world-tab-jobs"
						class="focus-ring"
						tabindex="0"
					>
						{#if jobs.length === 0}
							<CarbonEmptyState
								title="No import jobs"
								description="Imports create durable, one-shot jobs with verification results."
							/>
						{:else}
							<div class="overflow-x-auto">
								<table class="w-full border-collapse text-left text-sm">
									<thead class="border-b border-border text-xs text-muted-foreground uppercase">
										<tr>
											<th scope="col" class="px-3 py-2">Job</th>
											<th scope="col" class="px-3 py-2">Phase</th>
											<th scope="col" class="px-3 py-2">Source</th>
											<th scope="col" class="px-3 py-2">Result</th>
										</tr>
									</thead>
									<tbody class="divide-y divide-border">
										{#each jobs as job (job.id)}
											<tr>
												<td class="px-3 py-2.5 font-mono text-xs break-all text-foreground">
													{job.id}
												</td>
												<td class="px-3 py-2.5">
													<CarbonTag type={importPhasePresentation(job.phase).tone} size="sm">
														{importPhasePresentation(job.phase).label}
													</CarbonTag>
												</td>
												<td class="px-3 py-2.5 text-xs text-muted-foreground">
													<span>{job.sourcePlatform || '—'}</span>
													<span class="block font-mono break-all">{job.sourceUrl || '—'}</span>
												</td>
												<td class="px-3 py-2.5 text-xs break-all text-muted-foreground">
													{#if job.error}
														<span class="text-destructive-text">{job.error}</span>
													{:else}
														<span class="font-mono">{shortId(job.resultRevisionId)}</span>
													{/if}
												</td>
											</tr>
										{/each}
									</tbody>
								</table>
							</div>
						{/if}
					</div>
				{/if}
			</div>
		</div>
	{/if}
</div>

<CarbonConfirm
	bind:open={bindOpen}
	title="Bind this revision?"
	message="The server is stopped, the revision is published as the new level, and the server restarts with its own writable copy. The stored revision is never modified."
	confirmLabel="Bind revision"
	confirming={bindBusy}
	onconfirm={() => void confirmBind()}
	onclose={() => (bindOpen = false)}
>
	{#snippet details()}
		<div class="space-y-3">
			<CarbonTextInput label="Server ID" bind:value={bindServerId} disabled={bindBusy} />
			<CarbonTextInput label="Level name" bind:value={bindLevelName} disabled={bindBusy} />
			<div>
				<label for="bind-window" class="block text-xs text-foreground">Bind window</label>
				<select
					id="bind-window"
					bind:value={bindWindow}
					disabled={bindBusy}
					class="focus-ring mt-1 h-10 w-full border border-input bg-background px-3 text-sm text-foreground"
				>
					<option value="now">Now (server is stopped for the swap)</option>
					<option value="next_restart">Next restart</option>
					<option value="idle">When idle</option>
				</select>
			</div>
			<p class="font-mono text-xs break-all text-muted-foreground">
				Revision {selectedRevisionId}
			</p>
		</div>
	{/snippet}
</CarbonConfirm>

<CarbonConfirm
	bind:open={unbindOpen}
	title="Remove this binding?"
	message="The binding is removed without touching world bytes. The server keeps its current files until a new revision is bound."
	confirmLabel="Remove binding"
	danger={true}
	confirming={unbindBusy}
	onconfirm={() => void confirmUnbind()}
	onclose={() => {
		pendingUnbind = null;
		unbindOpen = false;
	}}
>
	{#snippet details()}
		{#if pendingUnbind}
			<span class="font-mono break-all">
				{pendingUnbind.serverId} / {pendingUnbind.levelName}
			</span>
		{/if}
	{/snippet}
</CarbonConfirm>
