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
	import {
		Dialog,
		DialogContent,
		DialogDescription,
		DialogFooter,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { Dialog as DialogPrimitive } from 'bits-ui';
	import { CarbonButton } from '$lib/components/carbon';
	import { rpcClient } from '$lib/api/rpc-client';
	import { toast } from 'svelte-sonner';
	import { CarbonConfirm } from '$lib/components/carbon';
	import { Loader2, Save, X, Maximize2, Minimize2 } from '@lucide/svelte';
	import type { FileInfo } from '$lib/proto/carbonpanel/v1/file_pb';
	import { untrack } from 'svelte';
	import { FileEditorSession, type FileEditorState } from '$lib/features/files/editor-session';
	import * as monaco from 'monaco-editor';
	import editorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
	import jsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker';
	import cssWorker from 'monaco-editor/esm/vs/language/css/css.worker?worker';
	import htmlWorker from 'monaco-editor/esm/vs/language/html/html.worker?worker';
	import tsWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker';

	// Configure Monaco Environment only once globally
	if (!self.MonacoEnvironment) {
		self.MonacoEnvironment = {
			getWorker(_, label: string) {
				if (label === 'json') {
					return new jsonWorker();
				}
				if (label === 'css' || label === 'scss' || label === 'less') {
					return new cssWorker();
				}
				if (label === 'html' || label === 'handlebars' || label === 'razor') {
					return new htmlWorker();
				}
				if (label === 'typescript' || label === 'javascript') {
					return new tsWorker();
				}
				return new editorWorker();
			}
		};
	}

	interface Props {
		serverId: string;
		file: FileInfo | null;
		open: boolean;
		onClose: () => void;
		onSave?: () => void;
	}

	let { serverId, file, open = false, onClose, onSave }: Props = $props();

	const fileSession = new FileEditorSession(
		{
			async readFile(identity) {
				const response = await rpcClient.file.getFile(identity);
				return new TextDecoder().decode(response.content);
			},
			writeFile({ serverId, path, content }) {
				return rpcClient.file.updateFile({
					serverId,
					path,
					content: new TextEncoder().encode(content)
				});
			},
			onLoadError() {
				toast.error('Failed to load file content');
				closeDialog();
			},
			onSaveError: () => toast.error('Failed to save file'),
			onSaved() {
				toast.success('File saved successfully');
				onSave?.();
			}
		},
		(nextState) => {
			sessionState = nextState;
		}
	);
	let sessionState = $state<FileEditorState>(fileSession.state);
	let content = $derived(sessionState.content);
	let loading = $derived(sessionState.loading);
	let saving = $derived(sessionState.saving);
	let isDirty = $derived(sessionState.dirty);
	let isFullscreen = $state(false);
	let editorContainer = $state<HTMLDivElement>();
	let editor = $state<monaco.editor.IStandaloneCodeEditor | null>(null);
	let resizeObserver = $state<ResizeObserver | null>(null);
	let contentLoaded = $derived(sessionState.loaded);

	// Props select an identity. File content belongs to that session, not current props.
	$effect.pre(() => {
		const selected = open && file && !file.isDir ? { serverId, path: file.path } : null;
		untrack(() => {
			if (selected && fileSession.isFor(selected)) return;
			disposeEditor();
			confirmDiscardOpen = false;
			if (selected) {
				void fileSession.open(selected);
			} else {
				fileSession.close();
				isFullscreen = false;
			}
		});
	});

	// Create editor when content is loaded
	$effect(() => {
		if (open && editorContainer && contentLoaded && !editor && !loading) {
			createEditor();
		}
	});

	// Cleanup on unmount
	$effect(() => {
		return () => {
			fileSession.close();
			disposeEditor();
		};
	});

	function disposeEditor() {
		editor?.dispose();
		editor = null;
		resizeObserver?.disconnect();
		resizeObserver = null;
	}

	async function handleSave() {
		if (!open || !file || !fileSession.isFor({ serverId, path: file.path })) return;
		await fileSession.save();
	}

	let confirmDiscardOpen = $state(false);

	function closeDialog() {
		fileSession.close();
		disposeEditor();
		confirmDiscardOpen = false;
		onClose();
	}

	function handleClose() {
		if (isDirty) {
			confirmDiscardOpen = true;
			return;
		}
		closeDialog();
	}

	function createEditor() {
		const editorIdentity = fileSession.state.identity;
		if (!editorContainer || !editorIdentity) return;

		// Determine if file is large (over 100KB)
		const isLargeFile = content.length > 100000;

		const instance = monaco.editor.create(editorContainer, {
			value: content,
			language: file ? getFileLanguage(file.name) : 'plaintext',
			theme: 'vs-dark',
			fontSize: 14,
			fontFamily: "'JetBrains Mono', 'Monaco', 'Consolas', 'Courier New', monospace",
			minimap: { enabled: !isFullscreen && !isLargeFile },
			scrollBeyondLastLine: false,
			wordWrap: isLargeFile ? 'off' : 'on',
			lineNumbers: 'on',
			renderWhitespace: 'selection',
			bracketPairColorization: { enabled: !isLargeFile },
			formatOnPaste: false, // Disable for performance
			formatOnType: false, // Disable for performance
			automaticLayout: false, // We'll handle resize manually
			fixedOverflowWidgets: true,
			suggest: {
				showWords: !isLargeFile,
				showSnippets: !isLargeFile
			},
			// Additional performance optimizations
			folding: !isLargeFile,
			renderLineHighlight: 'line',
			scrollbar: {
				useShadows: false // Improves scrolling performance
			},
			mouseWheelZoom: false,
			quickSuggestions: !isLargeFile
		});
		editor = instance;

		// Manual resize handling for better performance
		resizeObserver = new ResizeObserver(() => {
			editor?.layout();
		});
		resizeObserver.observe(editorContainer);

		// Add save shortcut
		instance.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
			if (fileSession.isCurrent(editorIdentity)) void handleSave();
		});

		// Track content changes
		instance.onDidChangeModelContent(() => {
			if (editor === instance && fileSession.isCurrent(editorIdentity)) {
				fileSession.setContent(instance.getValue());
			}
		});

		// Focus editor
		editor.focus();
	}

	function getFileLanguage(fileName: string): string {
		const ext = fileName.toLowerCase().split('.').pop() || '';
		const languageMap: Record<string, string> = {
			js: 'javascript',
			ts: 'typescript',
			jsx: 'javascript',
			tsx: 'typescript',
			json: 'json',
			yml: 'yaml',
			yaml: 'yaml',
			toml: 'toml',
			properties: 'properties',
			conf: 'conf',
			cfg: 'ini',
			ini: 'ini',
			xml: 'xml',
			html: 'html',
			css: 'css',
			scss: 'scss',
			sass: 'sass',
			less: 'less',
			md: 'markdown',
			py: 'python',
			java: 'java',
			cpp: 'cpp',
			c: 'c',
			h: 'c',
			cs: 'csharp',
			go: 'go',
			rs: 'rust',
			php: 'php',
			rb: 'ruby',
			lua: 'lua',
			sh: 'bash',
			bash: 'bash',
			zsh: 'bash',
			fish: 'bash',
			ps1: 'powershell',
			bat: 'batch',
			cmd: 'batch',
			dockerfile: 'dockerfile',
			makefile: 'makefile',
			gradle: 'groovy',
			groovy: 'groovy',
			kt: 'kotlin',
			swift: 'swift',
			r: 'r',
			scala: 'scala',
			sql: 'sql',
			pl: 'perl',
			vim: 'vim'
		};
		return languageMap[ext] || 'plaintext';
	}

	function toggleFullscreen() {
		isFullscreen = !isFullscreen;
		if (editor) {
			editor.updateOptions({
				minimap: { enabled: !isFullscreen }
			});
		}
	}
</script>

<Dialog {open} onOpenChange={(isOpen) => !isOpen && handleClose()}>
	<DialogContent
		showCloseButton={false}
		class={isFullscreen
			? 'flex h-[95vh] w-[95vw]! max-w-[95vw]! flex-col sm:max-w-[95vw]!'
			: 'flex h-[85vh] w-[90vw]! max-w-[90vw]! flex-col sm:max-w-[90vw]!'}
	>
		<div class="absolute top-4 right-4 flex gap-1">
			<button
				onclick={toggleFullscreen}
				title={isFullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}
				aria-label={isFullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}
				class="inline-flex h-9 w-9 items-center justify-center rounded-md text-sm font-medium ring-offset-background transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:outline-none disabled:pointer-events-none disabled:opacity-50"
			>
				{#if isFullscreen}
					<Minimize2 class="h-4 w-4" />
				{:else}
					<Maximize2 class="h-4 w-4" />
				{/if}
			</button>
			<DialogPrimitive.Close
				class="inline-flex h-9 w-9 items-center justify-center rounded-md text-sm font-medium ring-offset-background transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:outline-none disabled:pointer-events-none disabled:opacity-50"
			>
				<X class="h-4 w-4" />
				<span class="sr-only">Close</span>
			</DialogPrimitive.Close>
		</div>
		<DialogHeader class="shrink-0">
			<DialogTitle class="flex items-center gap-2">
				{#if file}
					{file.name}
					{#if isDirty}
						<span class="text-sm text-muted-foreground">●</span>
					{/if}
				{:else}
					File Editor
				{/if}
			</DialogTitle>
			<DialogDescription>
				{#if file}
					{file.path}
				{/if}
			</DialogDescription>
		</DialogHeader>

		<div class="relative min-h-0 flex-1 overflow-hidden rounded-md border bg-background">
			{#if loading}
				<div class="absolute inset-0 z-10 flex items-center justify-center bg-background/80">
					<Loader2 class="h-8 w-8 animate-spin" />
				</div>
			{/if}
			<div bind:this={editorContainer} class="h-full w-full"></div>
		</div>

		<DialogFooter class="shrink-0">
			<div class="flex w-full items-center justify-between">
				<div class="flex items-center gap-4 text-sm text-muted-foreground">
					<span>
						{#if file}
							{getFileLanguage(file.name).toUpperCase()}
						{:else}
							PLAIN TEXT
						{/if}
					</span>
					<span>
						{content.split('\n').length} lines, {content.length} characters
					</span>
					{#if isDirty}
						<span class="text-orange-500">● Modified</span>
					{:else}
						<span class="text-green-500">● Saved</span>
					{/if}
				</div>
				<div class="flex items-center gap-2">
					<span class="text-xs text-muted-foreground"> Ctrl+S to save </span>
					<CarbonButton kind="tertiary" onclick={handleClose}>
						<X class="mr-2 h-4 w-4" />
						Close
					</CarbonButton>
					<CarbonButton
						onclick={handleSave}
						disabled={!isDirty || saving || loading}
						kind={isDirty ? 'primary' : 'secondary'}
					>
						{#if saving}
							<Loader2 class="mr-2 h-4 w-4 animate-spin" />
						{:else}
							<Save class="mr-2 h-4 w-4" />
						{/if}
						Save
					</CarbonButton>
				</div>
			</div>
		</DialogFooter>
	</DialogContent>
</Dialog>
<CarbonConfirm
	bind:open={confirmDiscardOpen}
	title="Discard unsaved changes?"
	message="Your edits will be lost."
	confirmLabel="Discard Changes"
	danger
	onconfirm={closeDialog}
/>
