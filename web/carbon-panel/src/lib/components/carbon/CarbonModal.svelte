<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script module lang="ts">
	let scrollLocks = 0;
	let previousOverflow = '';
	let activeDialogs: HTMLDivElement[] = [];

	function lockBackgroundScroll() {
		if (scrollLocks++ === 0) {
			previousOverflow = document.body.style.overflow;
			document.body.style.overflow = 'hidden';
		}
		return () => {
			if (--scrollLocks === 0) document.body.style.overflow = previousOverflow;
		};
	}
</script>

<script lang="ts">
	import type { Snippet } from 'svelte';
	import { untrack } from 'svelte';
	import { fade, scale } from 'svelte/transition';
	import { cubicOut, cubicIn } from 'svelte/easing';
	import { X } from '@lucide/svelte';
	import { motionDuration } from '$lib/motion';
	import CarbonButton from './CarbonButton.svelte';

	interface Props {
		open?: boolean;
		title?: string;
		description?: string;
		primaryButtonText?: string;
		secondaryButtonText?: string;
		danger?: boolean;
		size?: 'sm' | 'md' | 'lg' | 'xl' | '2xl' | '3xl' | '4xl' | '5xl' | 'full';
		hasFooter?: boolean;
		/** Render the content without the default header/footer chrome. */
		bare?: boolean;
		/** Fill the viewport edge to edge, above the app header and sidebar. */
		fullscreen?: boolean;
		/**
		 * Element that regains focus on close. Use when the trigger is disabled or
		 * replaced between the opening click and the dialog mounting; otherwise the
		 * element focused at open is restored.
		 */
		restoreFocusTo?: HTMLElement | null;
		onprimary?: () => void;
		onsecondary?: () => void;
		onclose?: () => void;
		footer?: Snippet;
		children?: Snippet;
	}

	let {
		open = $bindable(false),
		title = '',
		description = '',
		primaryButtonText = 'Confirm',
		secondaryButtonText = 'Cancel',
		danger = false,
		size = '2xl',
		hasFooter = true,
		bare = false,
		fullscreen = false,
		restoreFocusTo = null,
		onprimary,
		onsecondary,
		onclose,
		footer,
		children
	}: Props = $props();

	const sizeClasses = {
		sm: 'max-w-sm',
		md: 'max-w-md',
		lg: 'max-w-lg',
		xl: 'max-w-xl',
		'2xl': 'max-w-2xl',
		'3xl': 'max-w-3xl',
		'4xl': 'max-w-4xl',
		'5xl': 'max-w-5xl',
		full: 'max-w-[95vw] w-[95vw] h-[90vh]'
	};

	let modalElement = $state<HTMLDivElement | null>(null);
	let previousActiveElement: HTMLElement | null = null;
	const titleId = `carbon-modal-title-${Math.random().toString(36).slice(2, 9)}`;
	const descId = `carbon-modal-desc-${Math.random().toString(36).slice(2, 9)}`;

	function handleClose() {
		open = false;
		onclose?.();
	}

	// Portal overlays to <body>. Rendering a fixed overlay inside <main> left it
	// in the page's stacking context: the fixed header and sidebar painted over
	// it, and the main scroll container clipped it. bits-ui dialogs already
	// portal for the same reason; CarbonModal must do it too.
	function portal(node: HTMLElement) {
		document.body.appendChild(node);
		return {
			destroy() {
				node.remove();
			}
		};
	}

	function handleBackdropClick(event: MouseEvent) {
		if (event.target === event.currentTarget) {
			handleClose();
		}
	}

	function handleKeydown(event: KeyboardEvent) {
		if (!open || !isTopDialog()) return;

		if (event.key === 'Escape') {
			event.preventDefault();
			event.stopPropagation();
			handleClose();
			return;
		}

		if (event.key === 'Tab' && modalElement) {
			const focusableSelectors = [
				'a[href]',
				'button:not([disabled])',
				'input:not([disabled])',
				'select:not([disabled])',
				'textarea:not([disabled])',
				'[tabindex]:not([tabindex="-1"])'
			].join(', ');

			const focusable = Array.from(
				modalElement.querySelectorAll<HTMLElement>(focusableSelectors)
			).filter((el) => el.offsetParent !== null || el.offsetWidth > 0 || el.offsetHeight > 0);

			if (focusable.length === 0) {
				event.preventDefault();
				return;
			}

			const first = focusable[0];
			const last = focusable[focusable.length - 1];

			if (event.shiftKey) {
				if (document.activeElement === first || !modalElement.contains(document.activeElement)) {
					last.focus();
					event.preventDefault();
				}
			} else {
				if (document.activeElement === last || !modalElement.contains(document.activeElement)) {
					first.focus();
					event.preventDefault();
				}
			}
		}
	}

	function isTopDialog() {
		return modalElement === activeDialogs.at(-1);
	}

	$effect(() => {
		if (!open || !modalElement) return;
		const dialog = modalElement;
		const overlay = dialog.parentElement;
		// Svelte can reopen the same portal while its closing transition is running.
		// Keep paint order and keyboard ownership aligned on each open.
		if (overlay?.parentElement === document.body && overlay !== document.body.lastElementChild) {
			document.body.append(overlay);
		}
		activeDialogs = [...activeDialogs, dialog];
		return () => {
			activeDialogs = activeDialogs.filter((entry) => entry !== dialog);
		};
	});

	$effect(() => {
		// Lock background scrolling while the dialog owns the viewport.
		if (!open || typeof document === 'undefined') return;
		return lockBackgroundScroll();
	});

	$effect(() => {
		if (open) {
			if (typeof document !== 'undefined') {
				previousActiveElement =
					untrack(() => restoreFocusTo) ?? (document.activeElement as HTMLElement | null);
				// Focus the modal or its first actionable element on open
				const focusTimer = setTimeout(() => {
					if (!modalElement || !isTopDialog() || modalElement.contains(document.activeElement))
						return;
					const focusableSelectors = [
						'input:not([disabled])',
						'button:not([disabled])',
						'select:not([disabled])',
						'textarea:not([disabled])',
						'a[href]',
						'[tabindex]:not([tabindex="-1"])'
					].join(', ');
					const firstFocusable = modalElement.querySelector<HTMLElement>(focusableSelectors);
					if (firstFocusable) {
						firstFocusable.focus();
					} else {
						modalElement.focus();
					}
				}, 50);
				return () => clearTimeout(focusTimer);
			}
		} else {
			const restore = previousActiveElement;
			previousActiveElement = null;
			const top = activeDialogs.at(-1);
			if (restore?.isConnected && (!top || top === modalElement || top.contains(restore))) {
				restore.focus();
			}
		}
	});
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<div
		use:portal
		class="fixed inset-0 z-50 flex items-center justify-center rounded-none bg-black/70 backdrop-blur-xs select-none {fullscreen
			? 'p-0'
			: 'p-4'}"
		transition:fade={{ duration: motionDuration(150), easing: cubicOut }}
		onclick={handleBackdropClick}
		onwheel={(event) => {
			// Keep wheel input from scrolling the page behind the dialog.
			if (event.target === event.currentTarget) event.preventDefault();
		}}
	>
		<div
			bind:this={modalElement}
			role="dialog"
			aria-modal="true"
			aria-labelledby={title ? titleId : undefined}
			aria-describedby={description ? descId : undefined}
			tabindex="-1"
			class="w-full {fullscreen
				? 'h-screen max-h-screen max-w-none'
				: `${sizeClasses[size]} max-h-[90vh] border border-border`} flex flex-col rounded-none bg-background shadow-[var(--carbon-shadow-overlay)] outline-none select-text focus:outline-none"
			in:scale={{ start: 0.97, opacity: 0, duration: motionDuration(200), easing: cubicOut }}
			out:scale={{ start: 0.97, opacity: 0, duration: motionDuration(150), easing: cubicIn }}
		>
			{#if bare && title}
				<h3 id={titleId} class="sr-only">{title}</h3>
			{/if}
			{#if bare && description}
				<p id={descId} class="sr-only">{description}</p>
			{/if}

			<!-- Header -->
			{#if !bare}
				<div
					class="flex items-start justify-between rounded-none border-b border-border bg-card p-4 sm:p-6"
				>
					<div>
						{#if description}
							<span id={descId} class="font-sans text-xs font-normal text-muted-foreground"
								>{description}</span
							>
						{/if}
						<h3 id={titleId} class="mt-1 font-sans text-xl font-semibold text-foreground">
							{title}
						</h3>
					</div>
					<button
						type="button"
						onclick={handleClose}
						class="focus-ring motion-press cursor-pointer rounded-none p-2 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
						aria-label="Close modal"
					>
						<X class="h-4 w-4" strokeWidth={1.5} aria-hidden="true" />
					</button>
				</div>
			{/if}

			<!-- Body -->
			<div
				class={bare
					? 'flex min-h-0 flex-1 flex-col overflow-hidden'
					: 'flex-1 space-y-4 overflow-y-auto rounded-none p-4 font-sans text-sm text-foreground sm:p-6'}
			>
				{@render children?.()}
			</div>

			<!-- Footer -->
			{#if !bare}
				{#if footer}
					<div class="rounded-none border-t border-border bg-card">
						{@render footer()}
					</div>
				{:else if hasFooter}
					<div class="flex items-center justify-end rounded-none border-t border-border bg-card">
						<CarbonButton
							kind="secondary"
							size="lg"
							class="w-1/2 justify-center rounded-none"
							onclick={() => {
								if (onsecondary) {
									onsecondary();
								} else {
									handleClose();
								}
							}}
						>
							{secondaryButtonText}
						</CarbonButton>
						<CarbonButton
							kind={danger ? 'danger' : 'primary'}
							size="lg"
							class="w-1/2 justify-center rounded-none"
							onclick={onprimary}
						>
							{primaryButtonText}
						</CarbonButton>
					</div>
				{/if}
			{/if}
		</div>
	</div>
{/if}
