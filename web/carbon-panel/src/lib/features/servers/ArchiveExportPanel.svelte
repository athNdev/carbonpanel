<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<!--
	Presentational export control. The container owns the RPC, download session
	and notifications; every string that states a contract (formats, disclosure,
	consistency) is passed in from archive-presentation.ts. No runtime imports.

	The actions therefore repeat CarbonButton's md control height instead of
	importing it: `h-[var(--carbon-control-md)]`, so the dialog's own actions stay
	40 px like the CarbonButton that opens it. Only that height is matched for
	"Try again"; it keeps its own compact secondary/mono treatment and does not
	claim CarbonButton's secondary colour or spacing. ArchiveExportPanel.test.ts
	pins the height and the primary action class set against CarbonButton.svelte's
	source.
-->
<script lang="ts">
	type ArchivePhase = 'idle' | 'preparing' | 'ready' | 'error';
	type FormatOption = { value: number; label: string };

	interface Props {
		serverName: string;
		running: boolean;
		formats: ReadonlyArray<FormatOption>;
		format: number;
		onformat: (value: number) => void;
		phase: ArchivePhase;
		filename: string;
		totalSizeLabel: string;
		error: string | null;
		disclosure: string;
		disclosureDetail: string;
		consistencyText: string;
		acknowledged: boolean;
		downloading?: boolean;
		onacknowledge: (value: boolean) => void;
		onprepare: () => void;
		ondownload: () => void;
	}

	let {
		serverName,
		running,
		formats,
		format,
		onformat,
		phase,
		filename,
		totalSizeLabel,
		error,
		disclosure,
		disclosureDetail,
		consistencyText,
		acknowledged,
		downloading = false,
		onacknowledge,
		onprepare,
		ondownload
	}: Props = $props();
</script>

<div class="space-y-4 font-sans text-xs text-foreground" data-testid="archive-export-panel">
	<p class="text-muted-foreground">
		Create a portable archive of <span class="font-mono text-foreground">{serverName}</span>. The
		archive contains this server's managed data directory.
	</p>

	{#if running}
		<p
			role="alert"
			class="border-l-4 border-warning bg-warning/10 p-3 text-warning-text"
			data-testid="archive-export-running"
		>
			This server is not stopped. Stop it before exporting — Carbon Panel will not stop a live
			server for you.
		</p>
	{/if}

	<fieldset class="space-y-2" data-testid="archive-export-formats">
		<legend
			class="mb-2 font-mono text-xs font-semibold tracking-wider text-muted-foreground uppercase"
		>
			Archive format
		</legend>
		<div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
			{#each formats as option (option.value)}
				<button
					type="button"
					role="radio"
					aria-checked={format === option.value}
					disabled={running || phase === 'preparing'}
					onclick={() => onformat(option.value)}
					class="focus-ring cursor-pointer rounded-none border p-3 text-left transition-colors {format ===
					option.value
						? 'border-primary bg-accent shadow-[inset_3px_0_0_var(--primary)]'
						: 'border-border bg-background hover:bg-card'} disabled:cursor-not-allowed disabled:opacity-50"
				>
					<span class="font-mono text-xs font-semibold text-foreground">{option.label}</span>
				</button>
			{/each}
		</div>
	</fieldset>

	<p class="font-mono text-xs text-muted-foreground">Consistency: {consistencyText}</p>

	{#if phase === 'preparing'}
		<div class="border border-border bg-background p-4" role="status" aria-live="polite">
			<div class="flex items-center gap-3">
				<div class="h-4 w-4 animate-spin border-2 border-primary border-t-transparent"></div>
				<div>
					<p class="font-semibold text-foreground">Preparing archive…</p>
					<p class="mt-0.5 text-muted-foreground">
						Validation and compression can take several minutes. Keep this window open.
					</p>
				</div>
			</div>
		</div>
	{:else if phase === 'ready'}
		<div
			class="space-y-3 border border-border bg-background p-4"
			data-testid="archive-export-ready"
		>
			<h4 class="font-semibold text-foreground">Archive ready</h4>
			<div class="flex flex-wrap justify-between gap-2 font-mono text-xs">
				<span class="text-muted-foreground">{filename}</span>
				<span class="text-foreground">{totalSizeLabel}</span>
			</div>
			<div class="border-l-4 border-warning bg-warning/10 p-3" data-testid="archive-disclosure">
				<p class="font-semibold text-warning-text">{disclosure}</p>
				<p class="mt-1 text-muted-foreground">{disclosureDetail}</p>
				<label class="mt-3 flex cursor-pointer items-start gap-2 text-foreground">
					<input
						type="checkbox"
						checked={acknowledged}
						onchange={(event) => onacknowledge(event.currentTarget.checked)}
						class="mt-0.5 h-4 w-4 shrink-0 accent-primary"
					/>
					<span>I have reviewed this notice before downloading.</span>
				</label>
			</div>
			<div class="flex justify-end">
				<button
					type="button"
					disabled={!acknowledged || downloading}
					aria-busy={downloading ? 'true' : undefined}
					onclick={ondownload}
					class="focus-ring motion-press inline-flex h-[var(--carbon-control-md)] cursor-pointer items-center justify-center border border-transparent bg-primary px-4 font-sans text-sm font-normal tracking-[0.16px] whitespace-nowrap text-primary-foreground transition-[color,background-color,border-color,transform] select-none hover:bg-[var(--button-primary-hover)] active:bg-[var(--button-primary-active)] disabled:cursor-not-allowed disabled:opacity-50"
					data-testid="archive-download"
				>
					Download archive
				</button>
			</div>
		</div>
	{:else if phase === 'error'}
		<div class="space-y-3 border border-destructive bg-destructive/10 p-4" role="alert">
			<p class="text-destructive-text">{error ?? 'Archive preparation failed.'}</p>
			<button
				type="button"
				onclick={onprepare}
				class="focus-ring inline-flex h-[var(--carbon-control-md)] cursor-pointer items-center justify-center border border-border bg-background px-4 font-mono text-xs whitespace-nowrap text-foreground uppercase transition-colors select-none hover:bg-accent"
				data-testid="archive-retry"
			>
				Try again
			</button>
		</div>
	{/if}

	{#if phase === 'idle'}
		<div class="flex justify-end">
			<button
				type="button"
				disabled={running}
				onclick={onprepare}
				class="focus-ring motion-press inline-flex h-[var(--carbon-control-md)] cursor-pointer items-center justify-center border border-transparent bg-primary px-4 font-sans text-sm font-normal tracking-[0.16px] whitespace-nowrap text-primary-foreground transition-[color,background-color,border-color,transform] select-none hover:bg-[var(--button-primary-hover)] active:bg-[var(--button-primary-active)] disabled:cursor-not-allowed disabled:opacity-50"
				data-testid="archive-prepare"
			>
				Prepare export
			</button>
		</div>
	{/if}
</div>
