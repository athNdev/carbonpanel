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
		// offsetLeft is relative to the offset parent, not necessarily this scroll
		// container, so compare the tab's viewport rect with the container's
		// client area instead. A stale offset parent or transform used to leave a
		// deep-linked tab clipped at narrow widths.
		const containerRect = container.getBoundingClientRect();
		const buttonRect = button.getBoundingClientRect();
		const containerLeft = containerRect.left + container.clientLeft;
		const left = buttonRect.left - containerLeft;
		const right = buttonRect.right - containerLeft;
		if (left < 0) container.scrollLeft += left;
		else if (right > container.clientWidth) container.scrollLeft += right - container.clientWidth;
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
	class="scroll-strip relative flex items-center border-b border-border bg-background font-sans {className}"
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
			class="focus-ring relative z-10 flex h-[var(--carbon-control-md)] shrink-0 cursor-pointer items-center gap-2 border-b-2 border-transparent px-3 text-sm font-normal whitespace-nowrap transition-colors select-none {selectedTab ===
			tab.id
				? 'bg-card font-semibold text-foreground'
				: 'text-muted-foreground hover:bg-card hover:text-foreground'}"
		>
			<span>{tab.label}</span>
			{#if tab.badge !== undefined}
				<span class="bg-secondary px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
					{tab.badge}
				</span>
			{/if}
		</button>
	{/each}
	{#if indicatorWidth > 0}
		<!-- GPU-only motion: translateX + scaleX on a 1px base avoids transitioning
		     the layout `width` property, which is what made tab switches janky. -->
		<div
			class="pointer-events-none absolute bottom-0 left-0 z-20 h-0.5 w-px origin-left bg-primary transition-transform duration-[var(--motion-fast)] ease-[var(--ease-in-out-standard)]"
			style="transform: translateX({indicatorLeft}px) scaleX({indicatorWidth})"
		></div>
	{/if}
</div>
