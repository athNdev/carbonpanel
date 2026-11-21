<script lang="ts">
	import { Toaster as Sonner, type ToasterProps as SonnerProps } from 'svelte-sonner';
	import { CircleCheck, CircleX, Info, TriangleAlert, X } from '@lucide/svelte';
	import { theme as preferenceTheme } from '$lib/display-preferences.svelte';

	let { ...restProps }: SonnerProps = $props();
	let theme = $derived<'light' | 'dark'>($preferenceTheme === 'light' ? 'light' : 'dark');
</script>

<Sonner
	{theme}
	class="carbon-toaster"
	position="bottom-right"
	expand
	closeButton
	closeButtonAriaLabel="Dismiss notification"
	gap={8}
	offset="var(--carbon-space-5)"
	mobileOffset={{
		right: 'max(var(--carbon-space-3), env(safe-area-inset-right))',
		left: 'max(var(--carbon-space-3), env(safe-area-inset-left))',
		bottom: 'max(var(--carbon-space-3), env(safe-area-inset-bottom))'
	}}
	{...restProps}
>
	{#snippet successIcon()}<CircleCheck aria-hidden="true" />{/snippet}
	{#snippet errorIcon()}<CircleX aria-hidden="true" />{/snippet}
	{#snippet infoIcon()}<Info aria-hidden="true" />{/snippet}
	{#snippet warningIcon()}<TriangleAlert aria-hidden="true" />{/snippet}
	{#snippet closeIcon()}<X aria-hidden="true" />{/snippet}
</Sonner>

<style>
	:global(.carbon-toaster[data-sonner-toaster][data-sonner-theme]) {
		--normal-bg: var(--popover);
		--normal-text: var(--popover-foreground);
		--normal-border: var(--border);
		--border-radius: var(--radius);
		--toast-icon-margin-start: 0px;
		--toast-icon-margin-end: 0px;
		--toast-svg-margin-end: 0px;
		font-family: var(--carbon-font-sans);
	}

	:global(.carbon-toaster [data-sonner-toast][data-styled='true']) {
		--toast-accent: var(--accent-text);
		align-items: start;
		gap: var(--carbon-space-3);
		min-height: var(--carbon-control-lg);
		padding: var(--carbon-space-3);
		border-inline-start: 3px solid var(--toast-accent);
		box-shadow: var(--carbon-shadow-raised);
		font-size: 0.875rem;
	}

	:global(.carbon-toaster [data-sonner-toast][data-type='success']) {
		--toast-accent: var(--success-text);
	}

	:global(.carbon-toaster [data-sonner-toast][data-type='error']) {
		--toast-accent: var(--destructive-text);
	}

	:global(.carbon-toaster [data-sonner-toast][data-type='warning']) {
		--toast-accent: var(--warning-text);
	}

	:global(.carbon-toaster [data-sonner-toast][data-styled='true'] [data-icon]) {
		margin-top: 2px;
		color: var(--toast-accent);
	}

	:global(.carbon-toaster [data-sonner-toast] [data-icon] > svg) {
		width: 16px;
		height: 16px;
	}

	:global(.carbon-toaster [data-sonner-toast][data-styled='true'] [data-title]) {
		font-weight: 600;
	}

	:global(.carbon-toaster [data-sonner-toast][data-styled='true'] [data-description]) {
		color: var(--muted-foreground);
	}

	:global(.carbon-toaster [data-sonner-toast][data-styled='true']:has(> [data-close-button])) {
		padding-inline-end: calc(var(--carbon-control-sm) + var(--carbon-space-3));
	}

	:global(.carbon-toaster [data-sonner-toast][data-styled='true'] [data-close-button]) {
		inset: var(--carbon-space-2) auto auto auto;
		inset-inline-end: var(--carbon-space-2);
		width: var(--carbon-control-sm);
		height: var(--carbon-control-sm);
		transform: none;
		border: 0;
		border-radius: var(--radius);
		background: transparent;
		color: var(--muted-foreground);
	}

	:global(.carbon-toaster [data-sonner-toast][data-styled='true'] [data-close-button]:hover) {
		background: var(--secondary);
		color: var(--foreground);
	}

	:global(.carbon-toaster [data-sonner-toast] [data-close-button] > svg) {
		width: 16px;
		height: 16px;
	}

	:global(.carbon-toaster [data-sonner-toast]:focus-visible),
	:global(.carbon-toaster [data-sonner-toast] [data-close-button]:focus-visible) {
		outline: 2px solid var(--ring);
		outline-offset: 2px;
	}

	@media (max-width: 600px) {
		:global(.carbon-toaster[data-sonner-toaster][data-x-position='right']) {
			right: var(--mobile-offset-right);
			left: auto;
			width: min(var(--width), calc(100% - var(--mobile-offset-left) - var(--mobile-offset-right)));
		}

		:global(.carbon-toaster[data-sonner-toaster] [data-sonner-toast]) {
			width: 100%;
		}
	}
</style>
