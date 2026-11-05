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
	import { onMount, onDestroy } from 'svelte';
	import { ScrollText, RefreshCw, Download, ArrowDown } from '@lucide/svelte';
	import { toast } from 'svelte-sonner';
	import { rpcClient } from '$lib/api/rpc-client';
	import { formatFileSize, parseFileSize } from '$lib/utils/format-file-size';
	import { CarbonButton, CarbonInlineLoading, CarbonTag, CarbonTile } from '$lib/components/carbon';

	let loading = $state(true);
	let refreshing = $state(false);
	let logs = $state('');
	let filename = $state('');
	let fileSize = $state(0);
	let autoScroll = $state(true);
	let logContainer: HTMLPreElement | null = $state(null);
	let refreshInterval: ReturnType<typeof setInterval> | null = null;

	async function loadLogs(showToast = false) {
		if (refreshing) return;

		refreshing = true;
		try {
			const response = await rpcClient.support.getApplicationLogs({
				tail: 500 // Get last 500 lines
			});
			logs = response.content;
			filename = response.filename;
			fileSize = parseFileSize(response.size);

			if (showToast) {
				toast.success('Logs refreshed');
			}

			// Auto-scroll to bottom
			if (autoScroll && logContainer) {
				setTimeout(() => {
					if (logContainer) {
						logContainer.scrollTop = logContainer.scrollHeight;
					}
				}, 50);
			}
		} catch (error) {
			const message = error instanceof Error ? error.message : 'Unknown error occurred';
			if (showToast) {
				toast.error('Failed to load logs', { description: message });
			}
			console.error('Failed to load logs:', error);
		} finally {
			loading = false;
			refreshing = false;
		}
	}

	function downloadLogs() {
		if (!logs) return;

		const blob = new Blob([logs], { type: 'text/plain' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = filename || 'carbon-panel.log';
		a.click();
		URL.revokeObjectURL(url);
		toast.success('Logs downloaded');
	}

	function scrollToBottom() {
		if (logContainer) {
			logContainer.scrollTop = logContainer.scrollHeight;
		}
	}

	onMount(() => {
		loadLogs();
		// Auto-refresh every 5 seconds
		refreshInterval = setInterval(() => loadLogs(), 5000);
	});

	onDestroy(() => {
		if (refreshInterval) {
			clearInterval(refreshInterval);
		}
	});
</script>

<CarbonTile class="rounded-none !border-[#393939] !bg-[#262626] !p-0">
	<div class="border-b border-[#393939] px-5 py-4">
		<div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
			<div class="min-w-0">
				<h2 class="font-sans text-base font-semibold text-[#f4f4f4]">Application Logs</h2>
				<p class="mt-1 font-sans text-xs text-[#8d8d8d]">
					View real-time Carbon Panel application logs for debugging and monitoring.
				</p>
			</div>
			{#if filename}
				<div class="flex flex-wrap items-center gap-2">
					<CarbonTag type="gray" size="sm">{filename}</CarbonTag>
					<CarbonTag type="cool-gray" size="sm">{formatFileSize(fileSize)}</CarbonTag>
				</div>
			{/if}
		</div>
	</div>

	<div class="space-y-4 p-5">
		<div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
			<div class="flex flex-wrap items-center gap-2">
				<CarbonButton
					kind="secondary"
					size="sm"
					class="gap-2"
					onclick={() => loadLogs(true)}
					disabled={refreshing}
				>
					<RefreshCw class="h-4 w-4 {refreshing ? 'animate-spin' : ''}" />
					Refresh
				</CarbonButton>
				<CarbonButton
					kind="secondary"
					size="sm"
					class="gap-2"
					onclick={downloadLogs}
					disabled={!logs}
				>
					<Download class="h-4 w-4" />
					Download
				</CarbonButton>
				<CarbonButton kind="secondary" size="sm" class="gap-2" onclick={scrollToBottom}>
					<ArrowDown class="h-4 w-4" />
					Scroll to Bottom
				</CarbonButton>
			</div>
			<label class="flex cursor-pointer items-center gap-2 font-sans text-sm text-[#a8a8a8]">
				<input
					id="logs-auto-scroll"
					type="checkbox"
					bind:checked={autoScroll}
					class="focus-ring h-4 w-4 shrink-0 cursor-pointer accent-[#0f62fe]"
				/>
				Auto-scroll
			</label>
		</div>

		<div class="overflow-hidden rounded-none border border-[#393939] bg-[#161616]">
			{#if loading}
				<div class="flex h-96 items-center justify-center">
					<CarbonInlineLoading description="Loading logs..." />
				</div>
			{:else if !logs}
				<div class="flex h-96 flex-col items-center justify-center gap-2 text-center">
					<p class="font-sans text-sm text-[#c6c6c6]">No logs available</p>
					<p class="max-w-sm font-sans text-xs text-[#8d8d8d]">
						File logging may not be enabled in your configuration.
					</p>
				</div>
			{:else}
				<pre
					bind:this={logContainer}
					class="h-96 overflow-auto p-4 font-mono text-xs break-all whitespace-pre-wrap text-[#42be65]">{logs}</pre>
			{/if}
		</div>

		<div class="flex gap-3 rounded-none border border-[#393939] bg-[#1e1e1e] p-4">
			<ScrollText class="mt-0.5 h-4 w-4 shrink-0 text-[#78a9ff]" />
			<div class="space-y-1 font-sans text-xs text-[#a8a8a8]">
				<p class="font-medium text-[#c6c6c6]">Log Information</p>
				<p class="leading-relaxed">
					Showing the last 500 lines of application logs. Logs auto-refresh every 5 seconds. For
					complete logs, use the Support tab to generate a support bundle or click Download.
				</p>
			</div>
		</div>
	</div>
</CarbonTile>
