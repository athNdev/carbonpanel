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
	import {
		displayPreferences,
		theme as themeStore,
		motion as motionStore,
		density as densityStore,
		type ThemeChoice,
		type MotionChoice,
		type DensityChoice
	} from '$lib/display-preferences.svelte';

	interface Props {
		open?: boolean;
		onclose?: () => void;
	}

	let { open = $bindable(false), onclose }: Props = $props();

	let theme = $derived($themeStore);
	let motion = $derived($motionStore);
	let density = $derived($densityStore);

	$effect(() => {
		// Re-read stored values when the dialog opens; the shell already owns
		// application on boot. The shared store validates and applies them.
		if (browser && open) displayPreferences.hydrate();
	});

	function handleClose() {
		open = false;
		onclose?.();
	}

	function resetDefaults() {
		displayPreferences.reset();
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
			value={theme}
			onchange={(event) => displayPreferences.setTheme(event.currentTarget.value as ThemeChoice)}
			helperText="Light or dark Carbon theme via data-carbon-theme"
		>
			<option value="dark">Dark</option>
			<option value="light">Light</option>
		</CarbonSelect>

		<CarbonSelect
			label="Motion"
			value={motion}
			onchange={(event) => displayPreferences.setMotion(event.currentTarget.value as MotionChoice)}
			helperText="Reduced motion disables non-essential animation"
		>
			<option value="full">Full motion</option>
			<option value="reduced">Reduced motion</option>
		</CarbonSelect>

		<CarbonSelect
			label="Density"
			value={density}
			onchange={(event) =>
				displayPreferences.setDensity(event.currentTarget.value as DensityChoice)}
			helperText="Compact density tightens control spacing"
		>
			<option value="comfortable">Comfortable</option>
			<option value="compact">Compact</option>
		</CarbonSelect>

		<div class="flex items-center justify-end gap-3 border-t border-[var(--color-border)] pt-4">
			<CarbonButton kind="tertiary" onclick={resetDefaults} class="rounded-none">
				Reset defaults
			</CarbonButton>
			<CarbonButton kind="primary" onclick={handleClose} class="rounded-none">Done</CarbonButton>
		</div>
	</div>
</CarbonModal>
