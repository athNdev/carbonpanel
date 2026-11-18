<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { CarbonTag } from '$lib/components/carbon';
	import { PlayerConfidence, type Observation } from '$lib/proto/carbonpanel/v1/player_pb';
	import { provenanceView } from './presentation';

	interface Props {
		observation?: Observation | null;
		/** Optional name of the field this provenance belongs to. */
		label?: string;
		compact?: boolean;
	}

	let { observation = null, label = '', compact = false }: Props = $props();

	const view = $derived(provenanceView(observation));

	function confidenceTag(confidence: PlayerConfidence): 'green' | 'blue' | 'yellow' | 'gray' {
		switch (confidence) {
			case PlayerConfidence.DETECTED:
				return 'green';
			case PlayerConfidence.INFERRED:
				return 'blue';
			case PlayerConfidence.CONFLICT:
			case PlayerConfidence.STALE:
				return 'yellow';
			default:
				return 'gray';
		}
	}
</script>

{#if view}
	<div
		class="flex flex-wrap items-center gap-x-2 gap-y-1 font-sans text-xs text-muted-foreground {compact
			? 'py-1'
			: 'py-2'}"
	>
		{#if label}
			<span class="mono-label-accent">{label}</span>
		{/if}
		<CarbonTag
			type={confidenceTag(observation?.confidence ?? PlayerConfidence.UNSPECIFIED)}
			size="sm"
		>
			{view.source}
		</CarbonTag>
		<CarbonTag size="sm" type={view.authoritative ? 'gray' : 'yellow'}>
			{view.confidence}
		</CarbonTag>
		<span>
			Observed:
			<time class="font-mono text-foreground">{view.observedAt}</time>
		</span>
		{#if view.applicability}
			<span>Applies to: {view.applicability}</span>
		{/if}
		{#if view.conflictNote}
			<span class="text-warning-text" title={view.conflictNote}>Conflict: {view.conflictNote}</span>
		{/if}
		{#if !view.authoritative}
			<span class="mono-label">Non-authoritative</span>
		{/if}
	</div>
{:else}
	<div class="flex flex-wrap items-center gap-2 py-1 font-sans text-xs text-muted-foreground">
		{#if label}
			<span class="mono-label-accent">{label}</span>
		{/if}
		<CarbonTag type="gray" size="sm">No provenance reported</CarbonTag>
		<span>Source and observation time are unavailable; value is not authoritative.</span>
	</div>
{/if}
