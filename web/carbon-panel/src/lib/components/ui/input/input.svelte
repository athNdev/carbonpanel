<!--
SPDX-License-Identifier: MIT AND MPL-2.0
This file includes DiscoPanel material under the MIT License,
Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
and still applies to that inherited material.
Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
If a copy of the MPL was not distributed with this file, You can obtain
one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import type { HTMLInputAttributes, HTMLInputTypeAttribute } from 'svelte/elements';
	import { cn, type WithElementRef } from '$lib/utils.js';

	type InputType = Exclude<HTMLInputTypeAttribute, 'file'>;

	type Props = WithElementRef<
		Omit<HTMLInputAttributes, 'type'> &
			({ type: 'file'; files?: FileList } | { type?: InputType; files?: undefined })
	>;

	let {
		ref = $bindable(null),
		value = $bindable(),
		type,
		files = $bindable(),
		class: className,
		...restProps
	}: Props = $props();
</script>

{#if type === 'file'}
	<input
		bind:this={ref}
		data-slot="input"
		class={cn(
			'flex h-[var(--carbon-control-md)] w-full min-w-0 rounded-none border border-input bg-background px-3 py-2 text-sm text-foreground shadow-none transition-colors outline-none placeholder:text-muted-foreground focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-[var(--ring)] disabled:cursor-not-allowed disabled:border-border disabled:bg-secondary disabled:text-muted-foreground',
			'aria-invalid:border-destructive aria-invalid:focus-visible:outline-[var(--destructive)]',
			className
		)}
		type="file"
		bind:files
		bind:value
		{...restProps}
	/>
{:else}
	<input
		bind:this={ref}
		data-slot="input"
		class={cn(
			'flex h-[var(--carbon-control-md)] w-full min-w-0 rounded-none border border-input bg-background px-3 py-2 text-sm text-foreground shadow-none transition-colors outline-none placeholder:text-muted-foreground focus-visible:outline-2 focus-visible:outline-offset-[-2px] focus-visible:outline-[var(--ring)] disabled:cursor-not-allowed disabled:border-border disabled:bg-secondary disabled:text-muted-foreground',
			'aria-invalid:border-destructive aria-invalid:focus-visible:outline-[var(--destructive)]',
			className
		)}
		{type}
		bind:value
		{...restProps}
	/>
{/if}
