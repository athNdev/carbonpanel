<!--
SPDX-License-Identifier: MIT AND MPL-2.0
This file includes DiscoPanel material under the MIT License,
Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
and still applies to that inherited material.
Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
If a copy of the MPL was not distributed with this file, You can obtain
one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts" module>
	import { cn, type WithElementRef } from '$lib/utils.js';
	import type { HTMLAnchorAttributes, HTMLButtonAttributes } from 'svelte/elements';
	import { type VariantProps, tv } from 'tailwind-variants';

	export const buttonVariants = tv({
		base: "focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-primary inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-none text-sm font-normal outline-none transition-colors disabled:pointer-events-none disabled:opacity-40 disabled:bg-secondary disabled:text-muted-foreground aria-disabled:pointer-events-none aria-disabled:opacity-40 [&_svg:not([class*='size-'])]:size-4 [&_svg]:pointer-events-none [&_svg]:shrink-0 cursor-pointer",
		variants: {
			variant: {
				default:
					'bg-primary text-primary-foreground hover:bg-[var(--button-primary-hover)] active:bg-[var(--button-primary-active)] border-none shadow-none',
				destructive:
					'bg-destructive text-primary-foreground hover:bg-[var(--button-danger-hover)] active:bg-[var(--button-danger-active)] border-none shadow-none',
				outline:
					'bg-transparent border border-primary text-accent-text hover:bg-primary hover:text-primary-foreground shadow-none',
				secondary:
					'bg-secondary text-foreground hover:bg-[var(--button-secondary-hover)] active:bg-[var(--button-secondary-active)] border-none shadow-none',
				ghost:
					'bg-transparent text-accent-text hover:bg-accent hover:text-foreground border-none shadow-none',
				link: 'text-accent-text underline-offset-4 hover:underline shadow-none'
			},
			size: {
				default: 'h-10 px-4 py-2 has-[>svg]:px-3',
				sm: 'h-8 gap-1.5 px-3 has-[>svg]:px-2 text-xs',
				lg: 'h-12 px-6 has-[>svg]:px-4 text-base',
				icon: 'size-10'
			}
		},
		defaultVariants: {
			variant: 'default',
			size: 'default'
		}
	});

	export type ButtonVariant = VariantProps<typeof buttonVariants>['variant'];
	export type ButtonSize = VariantProps<typeof buttonVariants>['size'];

	export type ButtonProps = WithElementRef<HTMLButtonAttributes> &
		WithElementRef<HTMLAnchorAttributes> & {
			variant?: ButtonVariant;
			size?: ButtonSize;
		};
</script>

<script lang="ts">
	let {
		class: className,
		variant = 'default',
		size = 'default',
		ref = $bindable(null),
		href = undefined,
		type = 'button',
		disabled,
		children,
		...restProps
	}: ButtonProps = $props();
</script>

{#if href}
	<!-- eslint-disable svelte/no-navigation-without-resolve -- generic component; callers must resolve -->
	<a
		bind:this={ref}
		data-slot="button"
		class={cn(buttonVariants({ variant, size }), className)}
		href={disabled ? undefined : href}
		aria-disabled={disabled}
		role={disabled ? 'link' : undefined}
		tabindex={disabled ? -1 : undefined}
		{...restProps}
	>
		{@render children?.()}
	</a>
	<!-- eslint-enable svelte/no-navigation-without-resolve -->
{:else}
	<button
		bind:this={ref}
		data-slot="button"
		class={cn(buttonVariants({ variant, size }), className)}
		{type}
		{disabled}
		{...restProps}
	>
		{@render children?.()}
	</button>
{/if}
