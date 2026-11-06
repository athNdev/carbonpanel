<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { browser } from '$app/environment';
	import CarbonModal from './carbon/CarbonModal.svelte';
	import CarbonButton from './carbon/CarbonButton.svelte';
	import CarbonSelect from './carbon/CarbonSelect.svelte';

	interface Props {
		open?: boolean;
		onclose?: () => void;
	}

	let { open = $bindable(false), onclose }: Props = $props();

	type ThemeChoice = 'dark' | 'light';
	type MotionChoice = 'full' | 'reduced';
	type DensityChoice = 'comfortable' | 'compact';

	const THEME_KEY = 'carbon-theme';
	const MOTION_KEY = 'carbon-motion';
	const DENSITY_KEY = 'carbon-density';

	let theme = $state<ThemeChoice>('dark');
	let motion = $state<MotionChoice>('full');
	let density = $state<DensityChoice>('comfortable');

	function readStored(key: string): string | null {
		if (!browser) return null;
		try {
			return localStorage.getItem(key);
		} catch {
			return null;
		}
	}

	function applyPrefs() {
		if (!browser) return;
		try {
			document.documentElement.setAttribute('data-carbon-theme', theme);
			document.documentElement.setAttribute('data-carbon-motion', motion);
			document.documentElement.setAttribute('data-carbon-density', density);
			localStorage.setItem(THEME_KEY, theme);
			localStorage.setItem(MOTION_KEY, motion);
			localStorage.setItem(DENSITY_KEY, density);
		} catch {
			// Preferences are best-effort: private mode must not break the dialog.
		}
	}

	function loadPrefs() {
		const storedTheme = readStored(THEME_KEY);
		const storedMotion = readStored(MOTION_KEY);
		const storedDensity = readStored(DENSITY_KEY);
		if (storedTheme === 'light' || storedTheme === 'dark') theme = storedTheme;
		if (storedMotion === 'full' || storedMotion === 'reduced') motion = storedMotion;
		if (storedDensity === 'comfortable' || storedDensity === 'compact') density = storedDensity;
	}

	$effect(() => {
		if (open) loadPrefs();
	});

	$effect(() => {
		// Apply immediately so the shell previews the choice without a save step.
		if (browser && open) applyPrefs();
	});

	function handleClose() {
		applyPrefs();
		open = false;
		onclose?.();
	}

	function resetDefaults() {
		theme = 'dark';
		motion = 'full';
		density = 'comfortable';
		applyPrefs();
	}
</script>

<CarbonModal
	bind:open
	title="Preferences"
	description="Local display preferences stored in this browser only"
	size="md"
	hasFooter={false}
	onclose={handleClose}
>
	<div class="space-y-4">
		<CarbonSelect
			label="Theme"
			bind:value={theme}
			helperText="Light or dark Carbon theme via data-carbon-theme"
		>
			<option value="dark">Dark</option>
			<option value="light">Light</option>
		</CarbonSelect>

		<CarbonSelect
			label="Motion"
			bind:value={motion}
			helperText="Reduced motion disables non-essential animation"
		>
			<option value="full">Full motion</option>
			<option value="reduced">Reduced motion</option>
		</CarbonSelect>

		<CarbonSelect
			label="Density"
			bind:value={density}
			helperText="Compact density tightens control spacing"
		>
			<option value="comfortable">Comfortable</option>
			<option value="compact">Compact</option>
		</CarbonSelect>

		<div class="flex items-center justify-end gap-3 border-t border-[#393939] pt-4">
			<CarbonButton kind="tertiary" onclick={resetDefaults} class="rounded-none">
				Reset defaults
			</CarbonButton>
			<CarbonButton kind="primary" onclick={handleClose} class="rounded-none">Done</CarbonButton>
		</div>
	</div>
</CarbonModal>
