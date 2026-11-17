<script lang="ts">
	import { Toaster as Sonner, type ToasterProps as SonnerProps } from 'svelte-sonner';
	import { theme as preferenceTheme } from '$lib/display-preferences.svelte';

	let { ...restProps }: SonnerProps = $props();
	let theme = $derived<'light' | 'dark'>($preferenceTheme === 'light' ? 'light' : 'dark');
</script>

<Sonner
	{theme}
	class="toaster group"
	style="--normal-bg: var(--color-popover); --normal-text: var(--color-popover-foreground); --normal-border: var(--color-border);"
	{...restProps}
/>
