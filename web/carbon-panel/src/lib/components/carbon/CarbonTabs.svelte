<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import { tick } from 'svelte';

	interface TabItem {
		id: string;
		label: string;
		badge?: string | number;
	}

	interface Props {
		tabs: TabItem[];
		selectedTab?: string;
		class?: string;
		/** Prefix for tab/panel element IDs; enables aria-controls wiring. */
		idPrefix?: string;
		onselect?: (id: string) => void;
	}

	let {
		tabs = [],
		selectedTab = $bindable(tabs[0]?.id || ''),
		class: className = '',
		idPrefix,
		onselect
	}: Props = $props();

	let container = $state<HTMLDivElement | null>(null);
	let indicatorLeft = $state(0);
	let indicatorWidth = $state(0);

	function measure() {
		if (!container) return;
		const buttons = container.querySelectorAll<HTMLButtonElement>('[data-tab]');
		const index = tabs.findIndex((tab) => tab.id === selectedTab);
		const button = buttons[index >= 0 ? index : 0];
		if (!button) {
			indicatorWidth = 0;
			return;
		}
		indicatorLeft = button.offsetLeft;
		indicatorWidth = button.offsetWidth;
		// Keep a selected tab visible after programmatic selection or resize,
		// without scrolling any ancestor or moving the page vertically.
		if (button.offsetLeft < container.scrollLeft) container.scrollLeft = button.offsetLeft;
		else if (button.offsetLeft + button.offsetWidth > container.scrollLeft + container.clientWidth)
			container.scrollLeft = button.offsetLeft + button.offsetWidth - container.clientWidth;
	}

	$effect(() => {
		// Re-run measurement when the tab list or selection changes.
		const _deps = [tabs, selectedTab];
		tick().then(() => requestAnimationFrame(measure));
	});

	$effect(() => {
		// The strip's width changes for reasons a window resize never reports:
		// the sidebar collapsing, a panel opening, a scrollbar appearing in an
		// ancestor. Measuring only on window resize left the indicator parked
		// under the previously selected tab after any of those. The observer
		// coalesces to one measurement per frame so the animation stays smooth.
		if (!container) return;
		let frame = 0;
		const schedule = () => {
			cancelAnimationFrame(frame);
			frame = requestAnimationFrame(measure);
		};
		const observer = new ResizeObserver(schedule);
		observer.observe(container);
		return () => {
			cancelAnimationFrame(frame);
			observer.disconnect();
		};
	});

	function handleTabClick(id: string) {
		selectedTab = id;
		onselect?.(id);
	}

	// APG tablist keyboard support: arrows move selection and focus.
	function handleTablistKeydown(event: KeyboardEvent) {
		if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
		event.preventDefault();
		const ids = tabs.map((tab) => tab.id);
		let index = ids.indexOf(selectedTab);
		if (index < 0) index = 0;
		if (event.key === 'ArrowRight') index = (index + 1) % ids.length;
		else if (event.key === 'ArrowLeft') index = (index - 1 + ids.length) % ids.length;
		else if (event.key === 'Home') index = 0;
		else index = ids.length - 1;
		handleTabClick(ids[index]);
		container?.querySelectorAll<HTMLButtonElement>('[data-tab]')[index]?.focus();
	}
</script>

<div
	bind:this={container}
	role="tablist"
	aria-label="Sections"
	onkeydown={handleTablistKeydown}
	data-carbon-tabs
	class="scroll-strip relative flex items-center border-b border-[#393939] bg-[#161616] font-sans {className}"
>
	{#each tabs as tab (tab.id)}
		<button
			type="button"
			role="tab"
			id={idPrefix ? `${idPrefix}-tab-${tab.id}` : undefined}
			aria-controls={idPrefix ? `${idPrefix}-panel-${tab.id}` : undefined}
			aria-selected={selectedTab === tab.id}
			tabindex={selectedTab === tab.id ? 0 : -1}
			data-tab
			data-testid="tab-{tab.id}"
			onclick={() => handleTabClick(tab.id)}
			class="relative z-10 flex h-10 shrink-0 cursor-pointer items-center gap-2 border-b-2 border-transparent px-3 text-sm font-normal whitespace-nowrap transition-colors select-none {selectedTab ===
			tab.id
				? 'bg-[#262626] font-semibold text-[#f4f4f4]'
				: 'text-[#a8a8a8] hover:bg-[#262626] hover:text-[#f4f4f4]'}"
		>
			<span>{tab.label}</span>
			{#if tab.badge !== undefined}
				<span class="bg-[#393939] px-1.5 py-0.5 font-mono text-xs text-[#c6c6c6]">
					{tab.badge}
				</span>
			{/if}
		</button>
	{/each}
	{#if indicatorWidth > 0}
		<!-- GPU-only motion: translateX + scaleX on a 1px base avoids transitioning
		     the layout `width` property, which is what made tab switches janky. -->
		<div
			class="pointer-events-none absolute bottom-0 left-0 z-20 h-0.5 w-px origin-left bg-[#0f62fe] transition-transform duration-[var(--motion-fast)] ease-[var(--ease-in-out-standard)]"
			style="transform: translateX({indicatorLeft}px) scaleX({indicatorWidth})"
		></div>
	{/if}
</div>
