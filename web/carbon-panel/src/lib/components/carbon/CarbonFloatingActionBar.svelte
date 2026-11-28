<!-- SPDX-License-Identifier: MPL-2.0 -->

<!--
	OSS-464: floating save/undo actions over server view sections.

	One bar lists every registered section that currently holds unsaved edits
	(sections stay mounted across tab switches, so several can be dirty while
	only one is visible). "Save" runs each section's save sequentially and
	reports the first failure; "Undo" reverts each section to its loaded
	snapshot without an RPC. The server page refuses gated lifecycle actions
	while sections are dirty and calls dirtySections.requestShake(), which
	re-fires the carbon-shake animation through the data-shake attribute and
	moves focus to the bar (brief B3: "the bar shakes, focus moves to it").
-->

<script lang="ts">
	import { fly } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import { Loader2, Save, Undo2 } from '@lucide/svelte';
	import { toast } from 'svelte-sonner';
	import { motionDuration } from '../../motion';
	import { dirtySectionEntries, dirtySectionShakeTick } from '../../stores/dirty-sections.svelte';

	interface Props {
		/** Deep-link a dirty section chip to its server-view tab. */
		onSelectSection?: (id: string) => void;
	}

	let { onSelectSection }: Props = $props();

	/** Two carbon-shake cycles at --motion-slow (300ms each). */
	const shakeMs = 600;

	let savingAll = $state(false);
	let shaking = $state(false);
	let announcement = $state('');
	let barHost = $state<HTMLDivElement | null>(null);

	// Reading isDirty()/saving() inside a $derived tracks each section's own
	// Svelte 5 state through the registered closure, so the bar follows edits
	// and in-flight saves without the registry being involved.
	const dirtyList = $derived($dirtySectionEntries.filter((entry) => entry.isDirty()));
	const dirtyCount = $derived(dirtyList.length);
	/** A section whose save request is in flight must not be double-submitted or undone. */
	const anySaving = $derived(dirtyList.some((entry) => entry.saving()));

	// Announce the bar appearing or changing so screen readers follow it.
	$effect(() => {
		if (dirtyCount === 0) return;
		announcement =
			dirtyCount === 1
				? `${dirtyList[0].label} has unsaved changes.`
				: `${dirtyCount} sections have unsaved changes.`;
	});

	// The page refuses a gated action and bumps the shake counter. Re-firing
	// the attribute from a clean frame keeps repeated refusals visible; under
	// reduced motion the duration snaps to zero and no reset timer is armed.
	// B3: the refusal also moves focus to the bar so keyboard users land on
	// the unsaved-changes context. Focus already inside the bar is kept.
	$effect(() => {
		if (!$dirtySectionShakeTick) return;
		announcement = 'Unsaved changes must be saved or undone first.';
		shaking = false;
		const frame = requestAnimationFrame(() => {
			shaking = true;
			if (barHost && !barHost.contains(document.activeElement)) {
				barHost.focus({ preventScroll: true });
			}
		});
		const duration = motionDuration(shakeMs);
		const timer = duration > 0 ? window.setTimeout(() => (shaking = false), duration) : undefined;
		return () => {
			cancelAnimationFrame(frame);
			if (timer !== undefined) clearTimeout(timer);
		};
	});

	async function saveAll() {
		if (savingAll || dirtyCount === 0) return;
		savingAll = true;
		const saved: string[] = [];
		let failure: { label: string; reason: string } | null = null;
		try {
			// Sequential by design: the first failure stops the run and reports.
			for (const entry of dirtyList) {
				if (failure) break;
				// dirtyList is the snapshot from when Save all started: a section
				// that became clean mid-run (its own chip's save completed while
				// this run goes) must be skipped — re-saving it would reject with
				// "No changes to save" and toast a spurious failure.
				if (!entry.isDirty()) continue;
				// A save already in flight from the section itself is skipped, not
				// double-submitted; it stays dirty until its own request settles.
				if (entry.saving()) continue;
				try {
					await entry.save();
					saved.push(entry.label);
				} catch (error) {
					failure = {
						label: entry.label,
						reason: error instanceof Error && error.message ? error.message : 'Unknown error'
					};
				}
			}
			if (failure) {
				const done = saved.length > 0 ? `Saved ${saved.join(', ')}. ` : '';
				const message = `${done}${failure.label} failed: ${failure.reason}`;
				toast.error(message);
				announcement = message;
			} else if (saved.length >= 2) {
				// Single-section saves keep their own success toasts (restart hints,
				// staging windows); the aggregate is only worth a toast for several.
				toast.success(`Saved ${saved.length} sections.`);
				announcement = `Saved ${saved.length} sections.`;
			} else if (saved.length === 1) {
				announcement = `Saved ${saved[0]}.`;
			}
		} finally {
			savingAll = false;
		}
	}

	function undoAll() {
		// Undoing while a save is in flight would race the post-save baseline.
		if (savingAll || anySaving || dirtyCount === 0) return;
		const message = `Reverted unsaved changes in ${dirtyCount} ${
			dirtyCount === 1 ? 'section' : 'sections'
		}.`;
		for (const entry of dirtyList) entry.undo();
		toast.success(message);
		announcement = message;
	}
</script>

{#if dirtyCount > 0}
	<div
		class="fixed right-0 bottom-0 left-0 z-50 pb-[env(safe-area-inset-bottom)] sm:right-auto sm:bottom-6 sm:left-1/2 sm:-translate-x-1/2 sm:pb-0"
		transition:fly={{ y: 8, duration: motionDuration(200), easing: cubicOut }}
	>
		<div
			bind:this={barHost}
			data-carbon-floating-action-bar="true"
			data-shake={shaking ? 'true' : undefined}
			role="group"
			aria-label="Unsaved changes"
			tabindex="-1"
			class="carbon-floating-action-bar mx-3 mb-3 max-w-full border border-border bg-card p-3 shadow-lg sm:mx-0 sm:mb-0 sm:max-w-2xl"
		>
			<p class="sr-only" role="status" aria-live="polite">{announcement}</p>
			<div class="flex flex-col gap-2 sm:flex-row sm:flex-wrap sm:items-center sm:gap-x-3">
				{#if dirtyCount > 1}
					<p class="font-mono text-xs whitespace-nowrap text-warning-text">
						{dirtyCount} sections with unsaved changes
					</p>
				{/if}
				<div class="flex min-w-0 flex-wrap items-center gap-1.5">
					{#each dirtyList as entry (entry.id)}
						<button
							type="button"
							class="focus-ring inline-flex min-h-10 max-w-full items-center gap-1.5 border border-border bg-background px-2.5 font-mono text-xs text-foreground transition-colors hover:bg-accent sm:min-h-8"
							title="Review unsaved changes in {entry.label}"
							onclick={() => onSelectSection?.(entry.id)}
						>
							{#if entry.saving()}
								<Loader2
									class="h-3 w-3 shrink-0 animate-spin text-accent-text"
									aria-hidden="true"
								/>
							{/if}
							<span class="truncate">{entry.label}</span>
						</button>
					{/each}
				</div>
				<div class="flex items-center gap-2 sm:ml-auto">
					<button
						type="button"
						class="focus-ring inline-flex min-h-10 cursor-pointer items-center gap-1.5 border border-border bg-card px-3 font-sans text-xs text-foreground transition-colors hover:bg-accent disabled:cursor-not-allowed disabled:opacity-50 sm:min-h-8"
						disabled={savingAll || anySaving}
						title={anySaving
							? 'Wait for the save in flight to finish before undoing'
							: 'Revert to the saved values'}
						onclick={undoAll}
					>
						<Undo2 class="h-3.5 w-3.5" aria-hidden="true" />
						<span>{dirtyCount > 1 ? 'Undo all' : 'Undo'}</span>
					</button>
					<button
						type="button"
						class="focus-ring inline-flex min-h-10 cursor-pointer items-center gap-1.5 bg-primary px-3 font-sans text-xs text-primary-foreground transition-colors hover:bg-[var(--button-primary-hover)] disabled:cursor-not-allowed disabled:opacity-50 sm:min-h-8"
						disabled={savingAll}
						title="Save the unsaved changes"
						onclick={saveAll}
					>
						{#if savingAll}
							<Loader2 class="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
						{:else}
							<Save class="h-3.5 w-3.5" aria-hidden="true" />
						{/if}
						<span>{dirtyCount > 1 ? 'Save all' : 'Save'}</span>
					</button>
				</div>
			</div>
		</div>
	</div>
{/if}
