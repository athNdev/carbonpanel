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
	import { untrack } from 'svelte';
	import type * as Monaco from 'monaco-editor';
	import { loadEditorRuntime } from '$lib/features/editor/runtime';

	interface Props {
		value?: string;
		language?: string;
		readOnly?: boolean;
		height?: string;
		onChange?: (value: string) => void;
	}

	let {
		value = '',
		language = 'json',
		readOnly = false,
		height = '300px',
		onChange
	}: Props = $props();

	let container = $state<HTMLDivElement>();
	let editor = $state<Monaco.editor.IStandaloneCodeEditor | null>(null);
	let monaco: typeof Monaco | undefined;
	let runtimeLoading = $state(true);
	let runtimeError = $state<string | null>(null);
	let resizeObserver: ResizeObserver | null = null;

	// Guards onChange from firing while we push an external value into the editor
	let applyingExternal = false;

	// Create the editor when the container mounts
	$effect(() => {
		if (!container) return;
		const element = container;
		let disposed = false;
		runtimeLoading = true;
		runtimeError = null;
		void loadEditorRuntime()
			.then((runtime) => {
				if (disposed) return;
				monaco = runtime;
				const instance = runtime.editor.create(element, {
					value: untrack(() => value),
					language: untrack(() => language),
					readOnly: untrack(() => readOnly),
					theme: 'vs-dark',
					fontSize: 12,
					fontFamily: "'JetBrains Mono', 'Fira Code', 'Monaco', 'Consolas', monospace",
					minimap: { enabled: false },
					scrollBeyondLastLine: false,
					wordWrap: 'on',
					lineNumbers: 'on',
					automaticLayout: false,
					fixedOverflowWidgets: true,
					scrollbar: { useShadows: false }
				});

				editor = instance;
				instance.onDidChangeModelContent(() => {
					if (applyingExternal || editor !== instance) return;
					onChange?.(instance.getValue());
				});

				resizeObserver = new ResizeObserver(() => editor?.layout());
				resizeObserver.observe(element);
				runtimeLoading = false;
			})
			.catch(() => {
				if (disposed) return;
				runtimeLoading = false;
				runtimeError = 'Failed to load editor';
			});

		return () => {
			disposed = true;
			resizeObserver?.disconnect();
			resizeObserver = null;
			const model = editor?.getModel();
			editor?.dispose();
			model?.dispose();
			editor = null;
		};
	});

	// Push external value changes into the editor
	$effect(() => {
		const next = value;
		if (editor && editor.getValue() !== next) {
			applyingExternal = true;
			editor.setValue(next);
			applyingExternal = false;
		}
	});

	// Reflect read-only and language changes without recreating
	$effect(() => {
		editor?.updateOptions({ readOnly });
	});

	$effect(() => {
		const model = editor?.getModel();
		if (model) monaco?.editor.setModelLanguage(model, language);
	});
</script>

<div
	bind:this={container}
	style="height: {height}; width: 100%;"
	class="overflow-hidden rounded-md border border-border/50"
></div>

{#if runtimeError}<p role="alert">{runtimeError}</p>{:else if runtimeLoading}<p role="status">
		Loading editor...
	</p>{/if}
