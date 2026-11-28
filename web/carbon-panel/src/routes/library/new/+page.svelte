<!--
SPDX-License-Identifier: MPL-2.0
-->

<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { toast } from 'svelte-sonner';
	import { newWorldRequestId, worldLibraryActions } from '$lib/stores/world-library';
	import { formatRpcError } from '$lib/api/rpc-error';
	import type { ConfigControl } from '$lib/proto/carbonpanel/v1/world_pb';
	import type { ConfigDraftPreview, WorldConfigSchema } from '$lib/proto/carbonpanel/v1/world_pb';
	import {
		classificationPresentation,
		isRestartRequiredClassification,
		isUnsafeClassification,
		confidencePresentation,
		modLoaderLabel
	} from '$lib/features/world-library/presentation';
	import {
		CarbonAccordion,
		CarbonAccordionItem,
		CarbonBreadcrumbs,
		CarbonButton,
		CarbonEmptyState,
		CarbonInlineLoading,
		CarbonPageHeader,
		CarbonSelect,
		CarbonTag,
		CarbonTextInput
	} from '$lib/components/carbon';
	import { AlertTriangle, Check, ChevronLeft, ChevronRight, WandSparkles } from '@lucide/svelte';

	interface WizardStep {
		id: string;
		label: string;
		groups: string[];
		tier?: 'advanced' | 'expert';
	}

	// The order is fixed by the product requirement. Controls are assigned to a
	// step from the schema's own `group` and tier flags, never from UI strings.
	const steps: WizardStep[] = [
		{ id: 'basic', label: 'Basic', groups: ['basic', 'identity', 'general', 'metadata'] },
		{ id: 'worldgen', label: 'Worldgen', groups: ['worldgen', 'generation', 'level', 'seed'] },
		{ id: 'gameplay', label: 'Gameplay', groups: ['gameplay', 'rules', 'gamerule', 'difficulty'] },
		{ id: 'datapacks', label: 'Datapacks', groups: ['datapack', 'datapacks', 'pack', 'packs'] },
		{ id: 'advanced', label: 'Advanced / Expert', groups: [], tier: 'advanced' }
	];

	function stepFor(control: ConfigControl): WizardStep {
		if (control.expert || control.advanced) return steps[steps.length - 1];
		const group = control.group.toLowerCase();
		for (const step of steps) {
			if (step.tier) continue;
			if (step.groups.some((keyword) => group.includes(keyword))) return step;
		}
		// Unknown groups stay visible instead of being silently dropped.
		return steps[0];
	}

	let name = $state('');
	let edition = $state('java');
	let mcVersion = $state('');
	let loader = $state('1');
	let dataVersion = $state('');
	let licenseAttestation = $state('');

	let schema = $state<WorldConfigSchema | null>(null);
	let schemaBusy = $state(false);
	let schemaError = $state<unknown>(null);
	let values = $state<Record<string, string>>({});
	let preset = $state('');
	let stepIndex = $state(0);
	let advancedOpen = $state(false);
	let expertOpen = $state(false);

	let preview = $state<ConfigDraftPreview | null>(null);
	let previewBusy = $state(false);
	let previewError = $state<unknown>(null);
	let createBusy = $state(false);
	let createError = $state<unknown>(null);

	let schemaEpoch = 0;

	onMount(() => {
		void loadSchema();
	});

	async function loadSchema() {
		const epoch = ++schemaEpoch;
		schemaBusy = true;
		schemaError = null;
		try {
			const outcome = await worldLibraryActions.getWorldConfigSchema({
				edition: edition.trim(),
				mcVersion: mcVersion.trim(),
				modLoader: Number(loader),
				dataVersion: dataVersion.trim()
			});
			if (epoch !== schemaEpoch) return;
			if (outcome.kind !== 'schema') return;
			schema = outcome.value ?? null;
			const defaults: Record<string, string> = {};
			for (const control of schema?.controls ?? []) defaults[control.key] = control.defaultValue;
			values = defaults;
			preview = null;
			preset = '';
		} catch (error) {
			if (epoch !== schemaEpoch) return;
			schemaError = error;
			schema = null;
			values = {};
		} finally {
			if (epoch === schemaEpoch) schemaBusy = false;
		}
	}

	let currentStep = $derived(steps[stepIndex]);
	let stepControls = $derived(
		(schema?.controls ?? []).filter((control) => stepFor(control).id === currentStep.id)
	);
	let stepCount = (step: WizardStep) =>
		(schema?.controls ?? []).filter((control) => stepFor(control).id === step.id).length;

	let canPreview = $derived(
		Boolean(schema) && Boolean(edition.trim()) && Boolean(mcVersion.trim()) && !schemaBusy
	);
	let canCreate = $derived(
		Boolean(name.trim()) &&
			Boolean(preview) &&
			(preview?.validationErrors.length ?? 1) === 0 &&
			(preview?.conflicts.length ?? 1) === 0
	);

	function applyPreset(id: string) {
		const selected = schema?.presets.find((candidate) => candidate.id === id);
		if (!selected) return;
		preset = id;
		values = { ...values, ...selected.values };
		preview = null;
	}

	function controlType(control: ConfigControl): 'select' | 'boolean' | 'number' | 'text' {
		if (control.options.length > 0) return 'select';
		const kind = control.valueType.toLowerCase();
		if (kind.includes('bool')) return 'boolean';
		if (
			kind.includes('int') ||
			kind.includes('float') ||
			kind.includes('double') ||
			kind.includes('number')
		)
			return 'number';
		return 'text';
	}

	async function runPreview() {
		previewBusy = true;
		previewError = null;
		preview = null;
		try {
			const outcome = await worldLibraryActions.previewWorldConfig({
				edition: edition.trim(),
				mcVersion: mcVersion.trim(),
				modLoader: Number(loader),
				dataVersion: dataVersion.trim(),
				values,
				preset
			});
			if (outcome.kind === 'previewed') preview = outcome.value ?? null;
		} catch (error) {
			previewError = error;
		} finally {
			previewBusy = false;
		}
	}

	async function createWorld() {
		if (!canCreate) return;
		createBusy = true;
		createError = null;
		try {
			const outcome = await worldLibraryActions.createWorld({
				name: name.trim(),
				edition: edition.trim(),
				mcVersion: mcVersion.trim(),
				modLoader: Number(loader),
				dataVersion: dataVersion.trim(),
				values,
				preset,
				licenseAttestation: licenseAttestation.trim(),
				requestId: newWorldRequestId('create')
			});
			if (outcome.kind !== 'created') return;
			toast.success('Immutable world revision created.');
			const id = outcome.value.entry?.id;
			if (id) await goto(resolve('/library/[id]', { id }));
			else await goto(resolve('/library'));
		} catch (error) {
			createError = error;
		} finally {
			createBusy = false;
		}
	}

	function controlProps(control: ConfigControl) {
		const presentation = classificationPresentation(control.classification);
		return {
			presentation,
			unsafe: isUnsafeClassification(control.classification),
			restart: isRestartRequiredClassification(control.classification),
			type: controlType(control)
		};
	}
</script>

<div class="space-y-6 font-sans text-foreground">
	<CarbonPageHeader
		title="Create world from configuration"
		description="Fields are grouped by capability schema and state when they apply."
	>
		{#snippet tag()}
			<CarbonTag type="blue" size="sm">CONFIGURATION</CarbonTag>
		{/snippet}
		{#snippet actions()}
			<CarbonButton kind="secondary" size="md" href={resolve('/library')} class="justify-center">
				Back to library
			</CarbonButton>
		{/snippet}
	</CarbonPageHeader>

	<CarbonBreadcrumbs
		items={[{ href: resolve('/library'), label: 'World Library' }, { label: 'Create from config' }]}
	/>

	<div class="grid gap-6 lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)] [&>*]:min-w-0">
		<div class="space-y-4">
			<section class="border border-border bg-card p-4" aria-labelledby="target-heading">
				<h2 id="target-heading" class="mono-label-strong">Target</h2>
				<div class="mt-3 space-y-3">
					<CarbonTextInput label="World name" bind:value={name} placeholder="My new world" />
					<div>
						<CarbonSelect label="Edition" bind:value={edition}>
							<option value="java">java</option>
							<option value="bedrock">bedrock</option>
						</CarbonSelect>
					</div>
					<CarbonTextInput
						label="Minecraft version"
						bind:value={mcVersion}
						placeholder="e.g. 1.21.1"
						helperText="The schema is keyed by edition, version, loader and data version."
					/>
					<div>
						<CarbonSelect label="Loader" bind:value={loader}>
							<option value="1">Vanilla</option>
							<option value="2">Forge</option>
							<option value="3">Fabric</option>
							<option value="4">Quilt</option>
							<option value="5">Paper</option>
						</CarbonSelect>
					</div>
					<CarbonTextInput
						label="Data version"
						bind:value={dataVersion}
						placeholder="Optional, e.g. 3953"
					/>
					<CarbonTextInput
						label="License attestation"
						bind:value={licenseAttestation}
						placeholder="Optional provenance for created content"
					/>
					<CarbonButton
						kind="secondary"
						size="sm"
						loading={schemaBusy}
						onclick={() => void loadSchema()}
						class="justify-center"
					>
						Reload schema
					</CarbonButton>
				</div>
			</section>

			{#if schemaError}
				<div
					role="alert"
					class="border border-destructive bg-card p-3 text-xs text-destructive-text"
				>
					{formatRpcError(schemaError, 'The capability schema could not be loaded.')}
				</div>
			{/if}

			{#if schema?.partial}
				<div class="border border-warning bg-card p-3 text-xs text-warning-text" role="status">
					<div class="flex items-start gap-2">
						<AlertTriangle class="mt-0.5 h-4 w-4 shrink-0" />
						<div>
							Some capabilities are not verifiable for this target. Findings explain why.
							{#each schema.observations as observation, index (index)}
								<p class="mt-1 font-mono break-all">
									{confidencePresentation(observation.confidence).label} · {observation.source} · {observation.note}
								</p>
							{/each}
						</div>
					</div>
				</div>
			{/if}

			{#if schema && schema.presets.length > 0}
				<section class="border border-border bg-card p-4" aria-labelledby="presets-heading">
					<h2 id="presets-heading" class="mono-label-strong">Presets</h2>
					<div class="mt-3 flex flex-wrap gap-2">
						{#each schema.presets as option (option.id)}
							<CarbonButton
								kind={preset === option.id ? 'primary' : 'tertiary'}
								size="sm"
								onclick={() => applyPreset(option.id)}
								class="justify-center"
							>
								{option.label}
							</CarbonButton>
						{/each}
					</div>
					{#if preset}
						<p class="mt-2 text-xs text-muted-foreground">
							{schema.presets.find((option) => option.id === preset)?.description || ''}
						</p>
					{/if}
				</section>
			{/if}
		</div>

		<div class="space-y-4">
			<nav
				aria-label="Configuration steps"
				class="flex flex-wrap gap-2 border border-border bg-card p-2"
			>
				{#each steps as step, index (step.id)}
					<button
						type="button"
						aria-current={stepIndex === index ? 'step' : undefined}
						onclick={() => (stepIndex = index)}
						class="focus-ring flex min-h-10 items-center gap-2 border px-3 text-left text-xs transition-colors {stepIndex ===
						index
							? 'border-primary bg-primary text-primary-foreground'
							: 'border-border bg-background text-foreground hover:bg-accent'}"
					>
						<span class="font-mono">{index + 1}</span>
						<span>{step.label}</span>
						<span class="font-mono opacity-70">{stepCount(step)}</span>
					</button>
				{/each}
			</nav>

			{#if schemaBusy && !schema}
				<CarbonInlineLoading description="Loading schema…" />
			{:else if !schema}
				<CarbonEmptyState
					title="No schema available"
					description="Set an edition and Minecraft version, then reload the schema."
				/>
			{:else}
				<section
					class="border border-border bg-card p-4"
					aria-labelledby="step-{currentStep.id}-heading"
				>
					<h2 id="step-{currentStep.id}-heading" class="text-base font-semibold text-foreground">
						{currentStep.label}
					</h2>
					<p class="mt-1 text-xs text-muted-foreground">
						Schema: {schema.edition || '—'}
						{schema.mcVersion || '—'} ·
						{modLoaderLabel(schema.modLoader)} · data {schema.dataVersion || '—'}
					</p>

					{#if schema.conflictRules.length > 0}
						<div
							class="mt-3 border border-border bg-background p-3 text-xs text-muted-foreground"
							role="status"
						>
							<span class="mono-label">Conflict rules</span>
							<ul class="mt-1 list-inside list-disc space-y-1 break-all">
								{#each schema.conflictRules as rule, index (index)}
									<li>{rule}</li>
								{/each}
							</ul>
						</div>
					{/if}

					{#if stepControls.length === 0}
						<p class="mt-4 text-xs text-muted-foreground">
							No controls are defined for this step in the selected schema.
						</p>
					{:else if currentStep.tier}
						<CarbonAccordion class="mt-3">
							<CarbonAccordionItem
								title="Advanced"
								subtitle="{stepControls.filter((control) => control.advanced && !control.expert)
									.length} controls"
								bind:open={advancedOpen}
							>
								<div class="space-y-3 p-3">
									{#each stepControls.filter((control) => control.advanced && !control.expert) as control (control.key)}
										{@render controlField(control)}
									{/each}
								</div>
							</CarbonAccordionItem>
							<CarbonAccordionItem
								title="Expert"
								subtitle="{stepControls.filter((control) => control.expert).length} controls"
								bind:open={expertOpen}
							>
								<div class="space-y-3 p-3">
									{#each stepControls.filter((control) => control.expert) as control (control.key)}
										{@render controlField(control)}
									{/each}
								</div>
							</CarbonAccordionItem>
						</CarbonAccordion>
					{:else}
						<div class="mt-3 space-y-3">
							{#each stepControls as control (control.key)}
								{@render controlField(control)}
							{/each}
						</div>
					{/if}

					<div
						class="mt-4 flex flex-wrap items-center justify-between gap-2 border-t border-border pt-4"
					>
						<CarbonButton
							kind="tertiary"
							size="sm"
							disabled={stepIndex === 0}
							onclick={() => (stepIndex = Math.max(0, stepIndex - 1))}
							class="justify-center gap-2"
						>
							<ChevronLeft class="h-4 w-4" />
							Previous
						</CarbonButton>
						{#if stepIndex < steps.length - 1}
							<CarbonButton
								kind="secondary"
								size="sm"
								onclick={() => (stepIndex = Math.min(steps.length - 1, stepIndex + 1))}
								class="justify-center gap-2"
							>
								Next
								<ChevronRight class="h-4 w-4" />
							</CarbonButton>
						{:else}
							<CarbonButton
								kind="primary"
								size="sm"
								loading={previewBusy}
								disabled={!canPreview}
								onclick={() => void runPreview()}
								class="justify-center gap-2"
							>
								<WandSparkles class="h-4 w-4" />
								Preview changes
							</CarbonButton>
						{/if}
					</div>
				</section>
			{/if}

			{#if previewError}
				<div
					role="alert"
					class="border border-destructive bg-card p-3 text-xs text-destructive-text"
				>
					{formatRpcError(previewError, 'The dry run could not be produced.')}
				</div>
			{/if}

			{#if preview}
				<section
					class="space-y-3 border border-border bg-card p-4"
					aria-labelledby="preview-heading"
				>
					<div class="flex flex-wrap items-center gap-2">
						<h2 id="preview-heading" class="text-base font-semibold text-foreground">
							Read-only preview
						</h2>
						<CarbonTag
							type={preview.validationErrors.length || preview.conflicts.length ? 'red' : 'green'}
							size="sm"
						>
							{preview.validationErrors.length || preview.conflicts.length ? 'BLOCKED' : 'VALID'}
						</CarbonTag>
					</div>
					<p class="text-xs text-muted-foreground" role="status">
						{preview.summary || 'Dry-run diff. Nothing has been created.'}
					</p>

					{#if preview.validationErrors.length > 0}
						<div
							role="alert"
							class="border border-destructive bg-background p-3 text-xs text-destructive-text"
						>
							<span class="mono-label-strong">Validation errors</span>
							<ul class="mt-1 list-inside list-disc space-y-1 break-all">
								{#each preview.validationErrors as message, index (index)}
									<li>{message}</li>
								{/each}
							</ul>
						</div>
					{/if}
					{#if preview.conflicts.length > 0}
						<div
							role="alert"
							class="border border-destructive bg-background p-3 text-xs text-destructive-text"
						>
							<span class="mono-label-strong">Conflicts</span>
							<ul class="mt-1 list-inside list-disc space-y-1 break-all">
								{#each preview.conflicts as message, index (index)}
									<li>{message}</li>
								{/each}
							</ul>
						</div>
					{/if}
					{#if preview.restartRequired.length > 0}
						<div
							class="border border-warning bg-background p-3 text-xs text-warning-text"
							role="status"
						>
							<span class="mono-label-strong">Restart required</span>
							<ul class="mt-1 list-inside list-disc space-y-1 break-all">
								{#each preview.restartRequired as message, index (index)}
									<li>{message}</li>
								{/each}
							</ul>
						</div>
					{/if}

					{#if preview.changes.length > 0}
						<div class="overflow-x-auto">
							<table class="w-full border-collapse text-left text-sm">
								<thead class="border-b border-border text-xs text-muted-foreground uppercase">
									<tr>
										<th scope="col" class="px-3 py-2">Setting</th>
										<th scope="col" class="px-3 py-2">Before → after</th>
										<th scope="col" class="px-3 py-2">Applies</th>
										<th scope="col" class="px-3 py-2">Reason</th>
									</tr>
								</thead>
								<tbody class="divide-y divide-border">
									{#each preview.changes as change, index (index)}
										<tr>
											<td class="px-3 py-2 font-mono text-xs break-all text-foreground">
												{change.key}
												<CarbonTag
													type={classificationPresentation(change.classification).tone}
													size="sm"
												>
													{classificationPresentation(change.classification).label}
												</CarbonTag>
											</td>
											<td class="px-3 py-2 font-mono text-xs break-all text-foreground">
												{change.before || '—'} → {change.after || '—'}
											</td>
											<td class="px-3 py-2 text-xs text-muted-foreground">
												{change.appliesWhen || '—'}
											</td>
											<td class="px-3 py-2 text-xs break-all text-muted-foreground">
												{change.reason || '—'}
											</td>
										</tr>
									{/each}
								</tbody>
							</table>
						</div>
					{/if}

					{#if createError}
						<div
							role="alert"
							class="border border-destructive bg-background p-3 text-xs text-destructive-text"
						>
							{formatRpcError(createError, 'The world could not be created.')}
						</div>
					{/if}
					<CarbonButton
						kind="primary"
						size="md"
						loading={createBusy}
						disabled={!canCreate || createBusy}
						onclick={() => void createWorld()}
						class="justify-center gap-2"
					>
						<Check class="h-4 w-4" />
						Create immutable revision
					</CarbonButton>
				</section>
			{/if}
		</div>
	</div>
</div>

{#snippet controlField(control: ConfigControl)}
	{@const meta = controlProps(control)}
	<div class="border border-border bg-background p-3">
		<div class="flex flex-wrap items-start justify-between gap-2">
			<label for="ctl-{control.key}" class="text-xs font-medium text-foreground">
				{control.label}
			</label>
			<CarbonTag type={meta.presentation.tone} size="sm">
				{meta.presentation.label}
			</CarbonTag>
		</div>
		<p class="mt-1 text-xs text-muted-foreground">{control.description}</p>
		<p class="mt-1 font-mono text-xs text-accent-text">
			Applies: {control.appliesWhen || meta.presentation.hint || 'See capability schema'}
		</p>
		{#if meta.restart}
			<p class="mt-1 flex items-center gap-1 text-xs text-warning-text">
				<AlertTriangle class="h-3.5 w-3.5" />
				This setting requires a restart before it takes effect.
			</p>
		{/if}
		{#if meta.unsafe}
			<p class="mt-1 text-xs text-destructive-text" role="status">
				Disabled: {meta.presentation.hint}. This value is not included in the draft.
			</p>
		{/if}
		<div class="mt-2 max-w-md">
			{#if meta.type === 'select'}
				<CarbonSelect
					id="ctl-{control.key}"
					label=""
					disabled={meta.unsafe}
					bind:value={values[control.key]}
				>
					{#each control.options as option (option)}
						<option value={option}>{option}</option>
					{/each}
				</CarbonSelect>
			{:else if meta.type === 'boolean'}
				<CarbonSelect
					id="ctl-{control.key}"
					label=""
					disabled={meta.unsafe}
					bind:value={values[control.key]}
				>
					<option value="true">true</option>
					<option value="false">false</option>
				</CarbonSelect>
			{:else}
				<CarbonTextInput
					id="ctl-{control.key}"
					label=""
					type={meta.type === 'number' ? 'number' : 'text'}
					disabled={meta.unsafe}
					bind:value={values[control.key]}
				/>
			{/if}
		</div>
	</div>
{/snippet}
