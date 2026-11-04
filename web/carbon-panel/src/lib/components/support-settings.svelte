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
	import { onMount } from 'svelte';
	import {
		CarbonButton,
		CarbonInlineLoading,
		CarbonTextInput,
		CarbonTile
	} from '$lib/components/carbon';
	import {
		Download,
		AlertCircle,
		CheckCircle2,
		FileArchive,
		Database,
		ScrollText,
		Send,
		Copy,
		ExternalLink,
		MessageSquare,
		Server,
		ChevronDown,
		ChevronUp
	} from '@lucide/svelte';
	import { toast } from 'svelte-sonner';
	import { SvelteSet } from 'svelte/reactivity';
	import { rpcClient } from '$lib/api/rpc-client';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { serversStore } from '$lib/stores/servers';
	import type { Server as ServerType } from '$lib/proto/carbonpanel/v1/common_pb';

	let generating = $state(false);
	let uploading = $state(false);
	let bundlePath = $state<string | null>(null);
	let referenceId = $state<string | null>(null);
	let discordUsername = $state('');
	let email = $state('');
	let githubUsername = $state('');
	let issueDescription = $state('');
	let stepsToReproduce = $state('');

	// Server selection
	let servers = $state<ServerType[]>([]);
	let selectedServerIds = new SvelteSet<string>();
	let serverSectionExpanded = $state(false);
	let loadingServers = $state(true);

	onMount(async () => {
		try {
			servers = await serversStore.fetchServers(true);
		} catch (error) {
			console.error('Failed to load servers:', error);
		} finally {
			loadingServers = false;
		}
	});

	function toggleServer(serverId: string) {
		if (selectedServerIds.has(serverId)) {
			selectedServerIds.delete(serverId);
		} else {
			selectedServerIds.add(serverId);
		}
	}

	function selectAllServers() {
		selectedServerIds.clear();
		for (const s of servers) {
			selectedServerIds.add(s.id);
		}
	}

	function clearServerSelection() {
		selectedServerIds.clear();
	}

	async function generateBundle(upload: boolean = false) {
		if (upload) {
			uploading = true;
		} else {
			generating = true;
		}

		bundlePath = null;
		referenceId = null;

		const serverIds = Array.from(selectedServerIds);

		try {
			if (upload) {
				const response = await rpcClient.support.uploadSupportBundle({
					includeLogs: true,
					includeConfigs: true,
					includeSystemInfo: true,
					serverIds,
					discordUsername: discordUsername.trim(),
					email: email.trim(),
					githubUsername: githubUsername.trim(),
					issueDescription: issueDescription.trim(),
					stepsToReproduce: stepsToReproduce.trim()
				});

				if (response.success && response.referenceId) {
					referenceId = response.referenceId;
					discordUsername = '';
					email = '';
					githubUsername = '';
					issueDescription = '';
					stepsToReproduce = '';
					toast.success('Support bundle uploaded successfully!', {
						description: 'Save your reference ID for support requests.'
					});
				} else {
					toast.error('Failed to upload support bundle', {
						description: response.message || 'Unknown error occurred'
					});
				}
			} else {
				const response = await rpcClient.support.generateSupportBundle({
					includeLogs: true,
					includeConfigs: true,
					includeSystemInfo: true,
					serverIds
				});

				if (response.bundleId) {
					bundlePath = response.bundleId;
					toast.success('Support bundle generated!', {
						description: 'Click the download button to save the bundle.'
					});
				} else {
					toast.error('Failed to generate support bundle', {
						description: response.message
					});
				}
			}
		} catch (error) {
			const message = error instanceof Error ? error.message : 'Unknown error occurred';
			const action = upload ? 'upload' : 'generate';
			toast.error(`Failed to ${action} support bundle`, {
				description: message
			});
		} finally {
			generating = false;
			uploading = false;
		}
	}

	async function downloadBundle() {
		if (!bundlePath) return;

		try {
			const response = await rpcClient.support.downloadSupportBundle({
				bundleId: bundlePath
			});
			// Create download link
			const blob = new Blob([new Uint8Array(response.content)], { type: response.mimeType });
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = response.filename;
			a.click();
			URL.revokeObjectURL(url);
			// Clear the bundle path after download
			bundlePath = null;
			toast.success('Support bundle downloaded!');
		} catch (error) {
			const message = error instanceof Error ? error.message : 'Unknown error occurred';
			toast.error('Failed to download support bundle', {
				description: message
			});
		}
	}

	async function copyReferenceId() {
		if (!referenceId) return;
		const success = await copyToClipboard(referenceId);
		if (success) {
			toast.success('Reference ID copied to clipboard!');
		} else {
			toast.error('Failed to copy to clipboard');
		}
	}
</script>

<div class="space-y-4">
	<!-- Section header -->
	<CarbonTile class="rounded-none !border-[#393939] !bg-[#262626] !p-0">
		<div class="p-5">
			<div class="min-w-0">
				<div class="flex items-center gap-2">
					<AlertCircle class="h-4 w-4 shrink-0 text-[#78a9ff]" />
					<h2 class="truncate font-sans text-base font-semibold text-[#f4f4f4]">Support Tools</h2>
				</div>
				<p class="mt-1 max-w-2xl font-sans text-xs text-[#8d8d8d]">
					Generate and upload support bundles to help troubleshoot issues with Carbon Panel.
				</p>
			</div>
		</div>
	</CarbonTile>

	<!-- Support Bundle Info -->
	<CarbonTile class="rounded-none !border-[#393939] !bg-[#262626] !p-0">
		<div class="border-b border-[#393939] px-5 py-4">
			<h3 class="font-sans text-base font-semibold text-[#f4f4f4]">
				What's included in your support bundle?
			</h3>
			<p class="mt-1 font-sans text-xs text-[#8d8d8d]">
				We collect essential diagnostic data to help resolve issues quickly.
			</p>
		</div>

		<!-- 1px gaps paint the Carbon dividers between cells -->
		<div class="grid grid-cols-1 gap-px bg-[#393939] md:grid-cols-3">
			<div class="bg-[#262626] px-5 py-4">
				<div class="flex items-center gap-2">
					<div
						class="flex h-8 w-8 shrink-0 items-center justify-center border border-[#393939] bg-[#161616]"
					>
						<ScrollText class="h-4 w-4 text-[#78a9ff]" />
					</div>
					<h4 class="font-sans text-sm font-semibold text-[#f4f4f4]">Application Logs</h4>
				</div>
				<p class="mt-2 font-sans text-xs leading-relaxed text-[#8d8d8d]">
					Recent log entries and error messages to track down issues.
				</p>
			</div>

			<div class="bg-[#262626] px-5 py-4">
				<div class="flex items-center gap-2">
					<div
						class="flex h-8 w-8 shrink-0 items-center justify-center border border-[#393939] bg-[#161616]"
					>
						<Database class="h-4 w-4 text-[#78a9ff]" />
					</div>
					<h4 class="font-sans text-sm font-semibold text-[#f4f4f4]">Database Snapshot</h4>
				</div>
				<p class="mt-2 font-sans text-xs leading-relaxed text-[#8d8d8d]">
					Current configuration and server data for analysis.
				</p>
			</div>

			<div class="bg-[#262626] px-5 py-4">
				<div class="flex items-center gap-2">
					<div
						class="flex h-8 w-8 shrink-0 items-center justify-center border border-[#393939] bg-[#161616]"
					>
						<FileArchive class="h-4 w-4 text-[#78a9ff]" />
					</div>
					<h4 class="font-sans text-sm font-semibold text-[#f4f4f4]">System Information</h4>
				</div>
				<p class="mt-2 font-sans text-xs leading-relaxed text-[#8d8d8d]">
					Version and environment details for compatibility checks.
				</p>
			</div>
		</div>
	</CarbonTile>

	<!-- Server Selection -->
	<CarbonTile class="rounded-none !border-[#393939] !bg-[#262626] !p-0">
		<button
			type="button"
			class="focus-ring flex w-full cursor-pointer items-center justify-between gap-3 px-5 py-4 text-left transition-colors hover:bg-[#353535]"
			aria-expanded={serverSectionExpanded}
			aria-controls="support-server-selection"
			onclick={() => (serverSectionExpanded = !serverSectionExpanded)}
		>
			<div class="flex min-w-0 items-start gap-3">
				<div
					class="flex h-8 w-8 shrink-0 items-center justify-center border border-[#393939] bg-[#161616]"
				>
					<Server class="h-4 w-4 text-[#78a9ff]" />
				</div>
				<div class="min-w-0">
					<h3 class="font-sans text-base font-semibold text-[#f4f4f4]">Server Selection</h3>
					<p class="mt-1 font-sans text-xs text-[#8d8d8d]">
						{#if selectedServerIds.size === 0}
							Include all servers (default)
						{:else}
							{selectedServerIds.size} server{selectedServerIds.size === 1 ? '' : 's'} selected
						{/if}
					</p>
				</div>
			</div>
			<div
				class="flex h-8 w-8 shrink-0 items-center justify-center border border-[#393939] bg-[#161616]"
			>
				{#if serverSectionExpanded}
					<ChevronUp class="h-4 w-4 text-[#c6c6c6]" />
				{:else}
					<ChevronDown class="h-4 w-4 text-[#c6c6c6]" />
				{/if}
			</div>
		</button>

		{#if serverSectionExpanded}
			<div id="support-server-selection" class="border-t border-[#393939] px-5 py-4">
				{#if loadingServers}
					<div class="flex items-center justify-center py-4">
						<CarbonInlineLoading description="Loading servers..." />
					</div>
				{:else if servers.length === 0}
					<p class="py-4 text-center font-sans text-xs text-[#8d8d8d]">
						No servers found. All available data will be included.
					</p>
				{:else}
					<div class="space-y-3">
						<div class="flex flex-wrap items-center justify-between gap-2">
							<p class="min-w-0 flex-1 font-sans text-xs text-[#8d8d8d]">
								Select specific servers to include their logs and configurations.
							</p>
							<div class="flex flex-wrap items-center gap-2">
								<CarbonButton kind="ghost" size="sm" onclick={selectAllServers}>
									Select all
								</CarbonButton>
								<CarbonButton
									kind="ghost"
									size="sm"
									onclick={clearServerSelection}
									disabled={selectedServerIds.size === 0}
								>
									Clear
								</CarbonButton>
							</div>
						</div>

						<div class="grid grid-cols-1 gap-2 sm:grid-cols-2 md:grid-cols-3">
							{#each servers as server (server.id)}
								{@const selected = selectedServerIds.has(server.id)}
								<label
									class="flex cursor-pointer items-start gap-3 border bg-[#161616] p-3 transition-colors {selected
										? 'border-[#0f62fe]'
										: 'border-[#393939] hover:border-[#525252]'}"
								>
									<input
										type="checkbox"
										class="focus-ring mt-0.5 h-4 w-4 shrink-0 cursor-pointer accent-[#0f62fe]"
										checked={selected}
										onchange={() => toggleServer(server.id)}
									/>
									<span class="min-w-0 flex-1">
										<span
											class="block truncate font-sans text-sm text-[#f4f4f4]"
											title={server.name}>{server.name}</span
										>
										<span class="mt-0.5 block truncate font-mono text-[11px] text-[#8d8d8d]">
											{server.mcVersion || 'Unknown version'}
										</span>
									</span>
								</label>
							{/each}
						</div>
					</div>
				{/if}
			</div>
		{/if}
	</CarbonTile>

	<!-- User Contact & Issue Information -->
	<CarbonTile class="rounded-none !border-[#393939] !bg-[#262626] !p-0">
		<div class="border-b border-[#393939] px-5 py-4">
			<div class="flex items-center gap-2">
				<MessageSquare class="h-4 w-4 shrink-0 text-[#78a9ff]" />
				<h3 class="font-sans text-base font-semibold text-[#f4f4f4]">
					Contact &amp; Issue Details
				</h3>
			</div>
			<p class="mt-1 font-sans text-xs text-[#8d8d8d]">
				Optional information to help us respond to your support request.
			</p>
		</div>

		<div class="space-y-4 p-5">
			<div class="grid grid-cols-1 gap-4 md:grid-cols-3">
				<CarbonTextInput
					id="discord"
					label="Discord username"
					placeholder="username#1234"
					bind:value={discordUsername}
					class="[&_input]:bg-[#161616]"
				/>
				<CarbonTextInput
					id="email"
					label="Email"
					type="email"
					placeholder="you@example.com"
					bind:value={email}
					class="[&_input]:bg-[#161616]"
				/>
				<CarbonTextInput
					id="github"
					label="GitHub username"
					placeholder="username"
					bind:value={githubUsername}
					class="[&_input]:bg-[#161616]"
				/>
			</div>

			<div class="grid grid-cols-1 gap-4 md:grid-cols-2">
				<div class="space-y-1">
					<label for="description" class="font-sans text-xs tracking-[0.32px] text-[#f4f4f4]">
						Issue Description
					</label>
					<textarea
						id="description"
						rows={3}
						placeholder="Describe the issue you're experiencing..."
						bind:value={issueDescription}
						class="focus-ring w-full resize-none rounded-none border border-[#393939] bg-[#161616] px-4 py-2 font-sans text-sm text-[#f4f4f4] transition-colors placeholder:text-[#6f6f6f] focus:border-[#8d8d8d]"
					></textarea>
				</div>

				<div class="space-y-1">
					<label for="steps" class="font-sans text-xs tracking-[0.32px] text-[#f4f4f4]">
						Steps to Reproduce
					</label>
					<textarea
						id="steps"
						rows={3}
						placeholder="1. Go to...&#10;2. Click on...&#10;3. See error..."
						bind:value={stepsToReproduce}
						class="focus-ring w-full resize-none rounded-none border border-[#393939] bg-[#161616] px-4 py-2 font-sans text-sm text-[#f4f4f4] transition-colors placeholder:text-[#6f6f6f] focus:border-[#8d8d8d]"
					></textarea>
				</div>
			</div>
		</div>
	</CarbonTile>

	<!-- Action Buttons -->
	<CarbonTile class="rounded-none !border-[#393939] !bg-[#262626] !p-0">
		<div class="p-5">
			<p class="font-mono text-[11px] tracking-wider text-[#8d8d8d] uppercase">Support bundle</p>
			<div class="mt-3 flex flex-col gap-3 sm:flex-row sm:flex-wrap">
				<CarbonButton
					kind="secondary"
					size="lg"
					class="w-full justify-center gap-2 sm:w-auto"
					onclick={() => generateBundle(false)}
					disabled={generating || uploading}
					loading={generating}
				>
					{#if !generating}
						<Download class="h-4 w-4" />
					{/if}
					<span>{generating ? 'Generating bundle...' : 'Generate and download'}</span>
				</CarbonButton>

				<CarbonButton
					kind="primary"
					size="lg"
					class="w-full justify-center gap-2 sm:w-auto"
					onclick={() => generateBundle(true)}
					disabled={generating || uploading}
					loading={uploading}
				>
					{#if !uploading}
						<Send class="h-4 w-4" />
					{/if}
					<span>{uploading ? 'Uploading...' : 'Upload to support'}</span>
				</CarbonButton>
			</div>
			<p class="mt-3 font-sans text-xs text-[#8d8d8d]">
				Generate keeps a local copy of the support data. Upload sends it directly to the support
				team.
			</p>
		</div>
	</CarbonTile>

	<!-- Download Ready -->
	{#if bundlePath}
		<div
			class="motion-fade-in flex flex-wrap items-center gap-3 border border-[#24a148] bg-[#262626] p-5"
		>
			<CheckCircle2 class="h-4 w-4 shrink-0 text-[#24a148]" />
			<div class="min-w-0 flex-1">
				<p class="font-sans text-sm font-semibold text-[#f4f4f4]">
					Support bundle ready for download
				</p>
				<p class="mt-1 font-sans text-xs text-[#8d8d8d]">Bundle will be deleted after download.</p>
			</div>
			<CarbonButton
				kind="secondary"
				size="sm"
				class="shrink-0 justify-center gap-2"
				onclick={downloadBundle}
			>
				<Download class="h-4 w-4" />
				<span>Download now</span>
			</CarbonButton>
		</div>
	{/if}

	<!-- Upload Success -->
	{#if referenceId}
		<div class="motion-fade-in border border-[#24a148] bg-[#262626]">
			<div class="flex items-start gap-3 p-5">
				<CheckCircle2 class="mt-0.5 h-4 w-4 shrink-0 text-[#24a148]" />
				<div class="min-w-0 flex-1 space-y-4">
					<div>
						<p class="font-sans text-sm font-semibold text-[#f4f4f4]">
							Support bundle uploaded successfully
						</p>
						<p class="mt-1 font-sans text-xs text-[#8d8d8d]">
							Include this reference ID when you contact support.
						</p>
					</div>

					<!-- Reference ID Display -->
					<div class="space-y-2">
						<p class="font-mono text-[11px] tracking-wider text-[#8d8d8d] uppercase">
							Reference ID
						</p>
						<div class="flex items-center gap-2">
							<code
								class="min-w-0 flex-1 truncate border border-[#393939] bg-[#161616] px-3 py-2 font-mono text-sm text-[#f4f4f4]"
							>
								{referenceId}
							</code>
							<CarbonButton
								kind="ghost"
								size="sm"
								iconOnly
								class="shrink-0"
								aria-label="Copy reference ID"
								onclick={copyReferenceId}
							>
								<Copy class="h-4 w-4" />
							</CarbonButton>
						</div>
					</div>

					<div class="border-t border-[#393939] pt-3">
						<p class="font-sans text-xs font-semibold text-[#f4f4f4]">
							Please include this reference ID when:
						</p>
						<ul class="mt-1 space-y-1 font-sans text-xs text-[#a8a8a8]">
							<li>Requesting help in our Discord server</li>
							<li>Creating an issue on GitHub</li>
							<li>Contacting support directly</li>
						</ul>
					</div>

					<a
						href="https://github.com/athNdev/carbon-panel"
						target="_blank"
						rel="noopener noreferrer"
						class="focus-ring inline-flex items-center gap-1 font-sans text-xs text-[#78a9ff] hover:text-[#a6c8ff] hover:underline"
					>
						For more info and links to Discord/GitHub, please visit our site!
						<ExternalLink class="h-3 w-3" />
					</a>
				</div>
			</div>
		</div>
	{/if}

	<!-- Privacy Notice -->
	<CarbonTile class="rounded-none !border-[#393939] !bg-[#262626] !p-0">
		<div class="flex items-start gap-3 bg-[#161616] p-5">
			<AlertCircle class="mt-0.5 h-4 w-4 shrink-0 text-[#8d8d8d]" />
			<div class="min-w-0 space-y-1">
				<p class="font-mono text-[11px] tracking-wider text-[#8d8d8d] uppercase">Privacy notice</p>
				<p class="font-sans text-xs leading-relaxed text-[#a8a8a8]">
					Support bundles contain server configurations, logs, and database information. While we
					take privacy seriously and handle your data securely, please review the bundle contents
					before uploading if you have sensitive information.
				</p>
			</div>
		</div>
	</CarbonTile>
</div>
