<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

<script lang="ts">
	import {
		Check,
		Database,
		Image,
		Layers,
		Link2,
		Loader2,
		Pencil,
		Pin,
		Plus,
		RefreshCw,
		Search,
		Sparkles,
		Trash2,
		Upload,
		X
	} from '@lucide/svelte';
	import { toast } from 'svelte-sonner';
	import {
		CarbonButton,
		CarbonConfirm,
		CarbonModal,
		CarbonSearch,
		CarbonTag,
		CarbonTile
	} from '$lib/components/carbon';
	import { apiFetch } from '$lib/api/fetch';
	import { uploadFile } from '$lib/utils/chunked-upload';
	import { formatBytes } from '$lib/utils';

	type Kind = 'datapack' | 'resourcepack' | 'shaderpack';
	type Side = 'both' | 'client' | 'server';

	interface ContentItem {
		slug: string;
		name: string;
		file_name: string;
		side: Side | '';
		platform: string;
		project_id: string;
		version_id: string;
		download_url: string;
		file_size?: number;
		pinned?: boolean;
		optional?: boolean;
		default?: boolean;
		option_description?: string;
		hash?: string;
		hash_format?: string;
		sha512?: string;
	}

	interface SearchResult {
		id: string;
		slug: string;
		title: string;
		description: string;
		icon_url: string;
		author: string;
		downloads: number;
	}

	interface ContentVersion {
		id: string;
		name: string;
		version_number: string;
		version_type: string;
		game_versions: string[];
		loaders: string[];
		file_name: string;
		download_url: string;
		file_size: number;
		hash_format: string;
		hash: string;
		sha512: string;
	}

	interface UpdateInfo {
		slug: string;
		name: string;
		kind: string;
		platform: string;
		current_version_id: string;
		latest_version_id: string;
		latest_file_name: string;
		latest_download_url: string;
		latest_hash_format?: string;
		latest_hash?: string;
		latest_sha512?: string;
		update_available: boolean;
		pinned: boolean;
	}

	interface Props {
		packId: string;
		kind: Kind;
		items: ContentItem[];
		mcVersion: string;
		loader: string;
		datapackFolder?: string;
		onchanged: () => Promise<void> | void;
	}

	let {
		packId,
		kind,
		items,
		mcVersion,
		loader,
		datapackFolder = 'datapacks',
		onchanged
	}: Props = $props();

	const meta = $derived.by(() => {
		switch (kind) {
			case 'datapack':
				return {
					title: 'Datapacks',
					singular: 'datapack',
					description:
						'World data packs bundled with the pack. Set the install folder to match your datapack loader (OpenLoader, Paxi, or a datapacks/ override).',
					icon: Database,
					defaultSide: 'server' as Side,
					accept: '.zip'
				};
			case 'resourcepack':
				return {
					title: 'Resource Packs',
					singular: 'resource pack',
					description:
						'Client resource packs installed into resourcepacks/. Optional packs can be toggled by the player at install time.',
					icon: Image,
					defaultSide: 'client' as Side,
					accept: '.zip'
				};
			default:
				return {
					title: 'Shader Packs',
					singular: 'shader pack',
					description:
						'Iris and OptiFine shader packs installed into shaderpacks/. Search Modrinth by shader loader or add any archive by URL.',
					icon: Sparkles,
					defaultSide: 'client' as Side,
					accept: '.zip'
				};
		}
	});

	const defaultForm = () => ({
		name: '',
		file_name: '',
		side: meta.defaultSide as Side,
		download_url: '',
		hash_format: 'sha512',
		hash: '',
		pinned: false,
		optional: false,
		default: false,
		option_description: ''
	});

	let filter = $state('');
	let checkingUpdates = $state(false);
	let updates = $state<Record<string, UpdateInfo>>({});
	let applyingUpdate = $state('');

	let addOpen = $state(false);
	let addTab = $state<'modrinth' | 'url' | 'upload'>('modrinth');
	let addForm = $state(defaultForm());
	let searchQuery = $state('');
	let searchLoader = $state('');
	let searchVersion = $state(mcVersion);
	let searching = $state(false);
	let searchResults = $state<SearchResult[]>([]);
	let searchError = $state('');
	let selectedProject = $state<SearchResult | null>(null);
	let versions = $state<ContentVersion[]>([]);
	let loadingVersions = $state(false);
	let selectedVersionId = $state('');
	let installing = $state(false);
	let installSide = $state<Side>(meta.defaultSide);
	let installPinned = $state(false);
	let installOptional = $state(false);

	let uploadInput = $state<HTMLInputElement | null>(null);
	let uploadTarget = $state<File | null>(null);
	let uploading = $state(false);

	let folderDraft = $state(datapackFolder);
	let savingFolder = $state(false);

	let editTarget = $state<ContentItem | null>(null);
	let editOpen = $state(false);
	let editForm = $state(defaultForm());
	let savingEdit = $state(false);

	let deleteTarget = $state<ContentItem | null>(null);
	let deleteOpen = $state(false);
	let deleting = $state(false);

	$effect(() => {
		folderDraft = datapackFolder;
	});

	$effect(() => {
		installSide = meta.defaultSide;
	});

	const filtered = $derived.by(() => {
		const query = filter.trim().toLowerCase();
		if (!query) return items;
		return items.filter((item) =>
			[item.name, item.file_name, item.slug, item.option_description ?? '']
				.join(' ')
				.toLowerCase()
				.includes(query)
		);
	});

	function sideTag(side: Side | ''): 'gray' | 'cyan' | 'green' {
		if (side === 'client') return 'cyan';
		if (side === 'server') return 'green';
		return 'gray';
	}

	function sideLabel(side: Side | ''): string {
		if (side === 'client') return 'CLIENT';
		if (side === 'server') return 'SERVER';
		return 'BOTH';
	}

	function platformTag(platform: string): 'blue' | 'gray' | 'purple' {
		if (platform === 'modrinth') return 'blue';
		if (platform === 'curseforge') return 'purple';
		return 'gray';
	}

	async function errorText(res: Response): Promise<string> {
		const text = (await res.text()).trim();
		return text || `HTTP ${res.status}`;
	}

	function openAdd(tab: 'modrinth' | 'url' | 'upload' = 'modrinth') {
		addTab = tab;
		addForm = defaultForm();
		searchQuery = '';
		searchError = '';
		searchResults = [];
		selectedProject = null;
		versions = [];
		selectedVersionId = '';
		uploadTarget = null;
		installPinned = false;
		installOptional = false;
		addOpen = true;
		if (tab === 'modrinth') void runSearch();
	}

	async function runSearch() {
		searching = true;
		searchError = '';
		searchResults = [];
		selectedProject = null;
		versions = [];
		try {
			const params = new URLSearchParams({
				kind,
				game_version: searchVersion || mcVersion,
				limit: '24'
			});
			if (searchQuery.trim()) params.set('query', searchQuery.trim());
			if (searchLoader) params.set('loader', searchLoader);
			const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/content/search?${params}`);
			if (!res.ok) throw new Error(await errorText(res));
			const data = await res.json();
			searchResults = data.results ?? [];
		} catch (err) {
			searchError = err instanceof Error ? err.message : 'Search failed';
		} finally {
			searching = false;
		}
	}

	async function chooseProject(project: SearchResult) {
		selectedProject = project;
		selectedVersionId = '';
		versions = [];
		loadingVersions = true;
		try {
			const params = new URLSearchParams({
				project_id: project.id,
				kind,
				game_version: searchVersion || mcVersion
			});
			if (searchLoader) params.set('loader', searchLoader);
			const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/content/versions?${params}`);
			if (!res.ok) throw new Error(await errorText(res));
			const data = await res.json();
			versions = data.versions ?? [];
			selectedVersionId = versions[0]?.id ?? '';
		} catch (err) {
			searchError = err instanceof Error ? err.message : 'Could not load versions';
		} finally {
			loadingVersions = false;
		}
	}

	async function installVersion() {
		const project = selectedProject;
		const version = versions.find((candidate) => candidate.id === selectedVersionId);
		if (!project || !version) return;
		installing = true;
		try {
			const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/content`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					kind,
					name: project.title,
					file_name: version.file_name,
					side: installSide,
					platform: 'modrinth',
					project_id: project.id,
					version_id: version.id,
					download_url: version.download_url,
					file_size: version.file_size,
					hash_format: version.hash_format,
					hash: version.hash,
					sha512: version.sha512,
					pinned: installPinned,
					optional: installOptional
				})
			});
			if (!res.ok) throw new Error(await errorText(res));
			toast.success(`Installed ${project.title}`);
			addOpen = false;
			await onchanged();
		} catch (err) {
			toast.error(err instanceof Error ? err.message : `Failed to add ${meta.singular}`);
		} finally {
			installing = false;
		}
	}

	async function addFromURL() {
		if (!addForm.name.trim() || !addForm.download_url.trim()) {
			toast.error('Name and download URL are required');
			return;
		}
		installing = true;
		try {
			const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/content/url`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					kind,
					name: addForm.name.trim(),
					file_name: addForm.file_name.trim(),
					download_url: addForm.download_url.trim(),
					side: addForm.side,
					hash_format: addForm.hash_format,
					hash: addForm.hash.trim(),
					pinned: addForm.pinned,
					optional: addForm.optional,
					default: addForm.default,
					option_description: addForm.option_description.trim()
				})
			});
			if (!res.ok) throw new Error(await errorText(res));
			toast.success(`Added ${addForm.name.trim()}`);
			addOpen = false;
			await onchanged();
		} catch (err) {
			toast.error(err instanceof Error ? err.message : `Failed to add ${meta.singular}`);
		} finally {
			installing = false;
		}
	}

	async function addFromUpload() {
		if (!uploadTarget) {
			toast.error('Choose a .zip archive first');
			return;
		}
		if (!addForm.name.trim()) {
			toast.error('Name is required');
			return;
		}
		uploading = true;
		try {
			const { sessionId } = await uploadFile(uploadTarget);
			const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/content/upload`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					kind,
					upload_session_id: sessionId,
					file_name: addForm.file_name.trim() || uploadTarget.name,
					name: addForm.name.trim(),
					side: addForm.side,
					pinned: addForm.pinned,
					optional: addForm.optional,
					default: addForm.default,
					option_description: addForm.option_description.trim()
				})
			});
			if (!res.ok) throw new Error(await errorText(res));
			toast.success(`Uploaded ${addForm.name.trim()}`);
			addOpen = false;
			uploadTarget = null;
			await onchanged();
		} catch (err) {
			toast.error(err instanceof Error ? err.message : 'Upload failed');
		} finally {
			uploading = false;
		}
	}

	function openEdit(item: ContentItem) {
		editTarget = item;
		editForm = {
			name: item.name,
			file_name: item.file_name,
			side: (item.side || meta.defaultSide) as Side,
			download_url: item.download_url,
			hash_format: item.hash_format || 'sha512',
			hash: item.hash || '',
			pinned: !!item.pinned,
			optional: !!item.optional,
			default: !!item.default,
			option_description: item.option_description || ''
		};
		editOpen = true;
	}

	async function saveEdit() {
		const target = editTarget;
		if (!target) return;
		savingEdit = true;
		try {
			const body: Record<string, unknown> = {
				name: editForm.name.trim(),
				file_name: editForm.file_name.trim(),
				side: editForm.side,
				pinned: editForm.pinned,
				optional: editForm.optional,
				default: editForm.default,
				option_description: editForm.option_description.trim()
			};
			// Only manual entries expose their download block in the editor.
			if (target.platform === 'url') {
				body.download_url = editForm.download_url.trim();
				body.hash_format = editForm.hash_format;
				body.hash = editForm.hash.trim();
			}
			const res = await apiFetch(
				`/api/v1/packwiz/packs/${packId}/content/${kind}/${encodeURIComponent(target.slug)}`,
				{
					method: 'PATCH',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify(body)
				}
			);
			if (!res.ok) throw new Error(await errorText(res));
			toast.success(`Updated ${editForm.name.trim()}`);
			editOpen = false;
			editTarget = null;
			await onchanged();
		} catch (err) {
			toast.error(err instanceof Error ? err.message : 'Update failed');
		} finally {
			savingEdit = false;
		}
	}

	async function confirmDelete() {
		const target = deleteTarget;
		if (!target) return;
		deleting = true;
		try {
			const res = await apiFetch(
				`/api/v1/packwiz/packs/${packId}/content/${kind}/${encodeURIComponent(target.slug)}`,
				{ method: 'DELETE' }
			);
			if (!res.ok) throw new Error(await errorText(res));
			toast.success(`Removed ${target.name}`);
			deleteOpen = false;
			deleteTarget = null;
			await onchanged();
		} catch (err) {
			toast.error(err instanceof Error ? err.message : 'Delete failed');
		} finally {
			deleting = false;
		}
	}

	async function checkUpdates() {
		checkingUpdates = true;
		try {
			const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/updates?kind=${kind}`);
			if (!res.ok) throw new Error(await errorText(res));
			const data = await res.json();
			const map: Record<string, UpdateInfo> = {};
			let available = 0;
			for (const update of data.updates ?? []) {
				map[update.slug] = update;
				if (update.update_available) available++;
			}
			updates = map;
			toast.success(
				available > 0
					? `${available} newer ${meta.singular} version${available === 1 ? '' : 's'} available`
					: `Every ${meta.singular} is up to date`
			);
		} catch (err) {
			toast.error(err instanceof Error ? err.message : 'Update check failed');
		} finally {
			checkingUpdates = false;
		}
	}

	async function applyUpdate(item: ContentItem) {
		const update = updates[item.slug];
		if (!update) return;
		applyingUpdate = item.slug;
		try {
			const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/updates`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					kind,
					slug: item.slug,
					target_version_id: update.latest_version_id,
					target_file_name: update.latest_file_name,
					target_url: update.latest_download_url,
					hash_format: update.latest_hash_format,
					hash: update.latest_hash,
					sha512: update.latest_sha512
				})
			});
			if (!res.ok) throw new Error(await errorText(res));
			toast.success(`Updated ${item.name}`);
			updates = { ...updates, [item.slug]: { ...update, update_available: false } };
			await onchanged();
		} catch (err) {
			toast.error(err instanceof Error ? err.message : 'Update failed');
		} finally {
			applyingUpdate = '';
		}
	}

	async function saveFolder() {
		const folder = folderDraft.trim();
		if (!folder || folder === datapackFolder) return;
		savingFolder = true;
		try {
			const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/datapack-folder`, {
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ folder })
			});
			if (!res.ok) throw new Error(await errorText(res));
			toast.success(`Datapacks now install into ${folder}`);
			await onchanged();
		} catch (err) {
			toast.error(err instanceof Error ? err.message : 'Could not move datapacks');
			folderDraft = datapackFolder;
		} finally {
			savingFolder = false;
		}
	}

	function chooseUpload(event: Event) {
		const input = event.target as HTMLInputElement;
		const file = input.files?.[0] ?? null;
		uploadTarget = file;
		if (file) {
			addForm.file_name = file.name;
			if (!addForm.name.trim()) {
				addForm.name = file.name.replace(/\.zip$/i, '');
			}
		}
	}
</script>

{#snippet contentIcon(className: string)}
	{#if kind === 'datapack'}
		<Database class={className} />
	{:else if kind === 'resourcepack'}
		<Image class={className} />
	{:else}
		<Sparkles class={className} />
	{/if}
{/snippet}

<CarbonTile class="rounded-none border-[#393939] p-0">
	<div
		class="flex flex-col justify-between gap-3 border-b border-[#393939] bg-[#262626] p-4 sm:flex-row sm:items-center"
	>
		<div class="min-w-0 space-y-0.5">
			<div class="flex items-center gap-2">
				{@render contentIcon('h-4 w-4 text-[#78a9ff]')}
				<h2 class="text-base font-semibold text-white">{meta.title} in Modpack</h2>
				<CarbonTag type="cyan" size="sm">{items.length}</CarbonTag>
			</div>
			<p class="text-xs text-[#a8a8a8]">{meta.description}</p>
		</div>

		<div class="flex flex-wrap items-center gap-2">
			<div class="w-44 sm:w-56">
				<CarbonSearch
					placeholder="Filter {meta.title.toLowerCase()}..."
					bind:value={filter}
					size="sm"
				/>
			</div>
			<CarbonButton
				kind="tertiary"
				size="sm"
				class="rounded-none"
				onclick={checkUpdates}
				disabled={checkingUpdates}
				title="Check Modrinth for newer releases"
			>
				{#if checkingUpdates}
					<Loader2 class="mr-1.5 h-3.5 w-3.5 animate-spin" />
				{:else}
					<Sparkles class="mr-1.5 h-3.5 w-3.5 text-amber-500" />
				{/if}
				Check Updates
			</CarbonButton>
			<CarbonButton kind="primary" size="sm" class="rounded-none" onclick={() => openAdd()}>
				<Plus class="mr-1.5 h-4 w-4" />
				Add {meta.title}
			</CarbonButton>
		</div>
	</div>

	{#if kind === 'datapack'}
		<div
			class="flex flex-col gap-2 border-b border-[#393939] bg-[#161616] p-3 sm:flex-row sm:items-center sm:justify-between"
		>
			<div class="min-w-0">
				<p class="font-mono text-[10px] tracking-wider text-[#8d8d8d] uppercase">Install folder</p>
				<p class="text-xs text-[#a8a8a8]">
					Written to pack.toml as <code class="font-mono">[options] datapack-folder</code>. Changing
					it moves existing datapack metadata.
				</p>
			</div>
			<div class="flex items-center gap-1.5">
				<input
					class="h-8 w-56 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
					bind:value={folderDraft}
					aria-label="Datapack install folder"
					spellcheck="false"
				/>
				<CarbonButton
					kind="secondary"
					size="sm"
					class="rounded-none"
					onclick={saveFolder}
					disabled={savingFolder || folderDraft.trim() === datapackFolder}
				>
					{#if savingFolder}
						<Loader2 class="mr-1.5 h-3.5 w-3.5 animate-spin" />
					{:else}
						<Check class="mr-1.5 h-3.5 w-3.5" />
					{/if}
					Save
				</CarbonButton>
			</div>
		</div>
	{/if}

	{#if filtered.length === 0}
		<div class="flex flex-col items-center justify-center gap-3 px-6 py-14 text-center">
			<div class="flex h-12 w-12 items-center justify-center border border-[#393939] bg-[#262626]">
				{@render contentIcon('h-5 w-5 text-[#8d8d8d]')}
			</div>
			<div>
				<p class="text-sm font-semibold text-[#f4f4f4]">
					{items.length === 0 ? `No ${meta.title.toLowerCase()} yet` : 'No matches'}
				</p>
				<p class="mt-1 text-xs text-[#a8a8a8]">
					{items.length === 0
						? `Add ${kind === 'datapack' ? 'a world datapack' : `a ${meta.singular}`} from Modrinth, a direct URL, or an upload.`
						: 'Clear the filter to see every entry.'}
				</p>
			</div>
			{#if items.length === 0}
				<CarbonButton kind="primary" size="sm" class="rounded-none" onclick={() => openAdd()}>
					<Plus class="mr-1.5 h-4 w-4" />
					Add {meta.title}
				</CarbonButton>
			{/if}
		</div>
	{:else}
		<div
			class="hidden items-center gap-3 border-b border-[#393939] bg-[#1e1e1e] px-4 py-2 font-mono text-[10px] tracking-wider text-[#8d8d8d] uppercase sm:flex"
		>
			<span class="min-w-0 flex-1">Name / file</span>
			<span class="w-16 text-center">Side</span>
			<span class="w-24 text-center">Options</span>
			<span class="w-24 text-center">Status</span>
			<span class="w-16 text-right">Actions</span>
		</div>

		<div class="divide-y divide-[#2a2a2a]">
			{#each filtered as item (item.slug)}
				{@const update = updates[item.slug]}
				<div
					class="group flex flex-col gap-2 px-4 py-3 transition-colors hover:bg-[#202020] sm:flex-row sm:items-center sm:gap-3"
				>
					<div class="flex min-w-0 flex-1 items-start gap-2.5">
						<div
							class="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center border border-[#393939] bg-[#161616]"
						>
							{@render contentIcon('h-4 w-4 text-[#78a9ff]')}
						</div>
						<div class="min-w-0">
							<div class="flex flex-wrap items-center gap-1.5">
								<span class="truncate text-sm font-medium text-[#f4f4f4]">{item.name}</span>
								<CarbonTag type={platformTag(item.platform)} size="sm">
									{item.platform.toUpperCase()}
								</CarbonTag>
								{#if item.pinned}
									<span class="inline-flex items-center gap-1 font-mono text-[10px] text-[#f1c21b]">
										<Pin class="h-3 w-3" /> PINNED
									</span>
								{/if}
								{#if item.optional}
									<CarbonTag type="purple" size="sm">OPTIONAL</CarbonTag>
								{/if}
								{#if item.optional && item.default}
									<CarbonTag type="green" size="sm">ON BY DEFAULT</CarbonTag>
								{/if}
							</div>
							<p class="truncate font-mono text-xs text-[#8d8d8d]" title={item.file_name}>
								{item.file_name}
							</p>
							{#if item.option_description}
								<p class="mt-0.5 line-clamp-2 text-xs text-[#a8a8a8]">{item.option_description}</p>
							{/if}
						</div>
					</div>

					<span class="w-16 shrink-0 text-center">
						<CarbonTag type={sideTag(item.side)} size="sm">{sideLabel(item.side)}</CarbonTag>
					</span>

					<span class="w-24 shrink-0 text-center font-mono text-[10px] text-[#8d8d8d]">
						{#if item.project_id && item.platform === 'modrinth'}
							{update?.update_available ? 'UPDATE' : 'TRACKED'}
						{:else}
							MANUAL
						{/if}
					</span>

					<span class="w-24 shrink-0 text-center">
						{#if update?.update_available}
							<CarbonButton
								kind="tertiary"
								size="sm"
								class="rounded-none"
								onclick={() => applyUpdate(item)}
								disabled={applyingUpdate === item.slug}
							>
								{#if applyingUpdate === item.slug}
									<Loader2 class="h-3.5 w-3.5 animate-spin" />
								{:else}
									<RefreshCw class="h-3.5 w-3.5" />
								{/if}
								<span class="ml-1">Update</span>
							</CarbonButton>
						{:else if update}
							<span class="font-mono text-[10px] text-[#6f6f6f]">UP TO DATE</span>
						{:else}
							<span class="font-mono text-[10px] text-[#6f6f6f]" title="Run Check Updates">—</span>
						{/if}
					</span>

					<span class="flex w-16 shrink-0 items-center justify-end gap-1">
						<button
							type="button"
							class="flex h-7 w-7 cursor-pointer items-center justify-center text-[#8d8d8d] transition-colors hover:bg-[#353535] hover:text-white"
							aria-label={`Edit options for ${item.name}`}
							title="Edit options"
							onclick={() => openEdit(item)}
						>
							<Pencil class="h-3.5 w-3.5" />
						</button>
						<button
							type="button"
							class="flex h-7 w-7 cursor-pointer items-center justify-center text-[#8d8d8d] transition-colors hover:bg-[#da1e28]/20 hover:text-[#ff8389]"
							aria-label={`Remove ${item.name}`}
							title="Remove"
							onclick={() => {
								deleteTarget = item;
								deleteOpen = true;
							}}
						>
							<Trash2 class="h-3.5 w-3.5" />
						</button>
					</span>
				</div>
			{/each}
		</div>
	{/if}
</CarbonTile>

<!-- Add dialog -->
<CarbonModal bind:open={addOpen} title="Add {meta.title}" size="4xl" hasFooter={false}>
	<div class="space-y-4">
		<div class="flex flex-wrap items-center gap-1 border-b border-[#393939] pb-2">
			<button
				type="button"
				class="border px-3 py-1.5 font-mono text-xs transition-colors {addTab === 'modrinth'
					? 'border-[#0f62fe] bg-[#0f62fe] text-white'
					: 'border-[#393939] bg-[#262626] text-[#c6c6c6] hover:bg-[#353535]'}"
				onclick={() => {
					addTab = 'modrinth';
					if (searchResults.length === 0) void runSearch();
				}}
			>
				Modrinth
			</button>
			<button
				type="button"
				class="border px-3 py-1.5 font-mono text-xs transition-colors {addTab === 'url'
					? 'border-[#0f62fe] bg-[#0f62fe] text-white'
					: 'border-[#393939] bg-[#262626] text-[#c6c6c6] hover:bg-[#353535]'}"
				onclick={() => (addTab = 'url')}
			>
				Direct URL
			</button>
			<button
				type="button"
				class="border px-3 py-1.5 font-mono text-xs transition-colors {addTab === 'upload'
					? 'border-[#0f62fe] bg-[#0f62fe] text-white'
					: 'border-[#393939] bg-[#262626] text-[#c6c6c6] hover:bg-[#353535]'}"
				onclick={() => (addTab = 'upload')}
			>
				Upload archive
			</button>
		</div>

		{#if addTab === 'modrinth'}
			<div class="flex flex-col gap-2 sm:flex-row sm:items-center">
				<div class="min-w-0 flex-1">
					<CarbonSearch
						bind:value={searchQuery}
						placeholder="Search {meta.title.toLowerCase()}..."
						size="sm"
					/>
				</div>
				<input
					class="h-8 w-28 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
					bind:value={searchVersion}
					aria-label="Minecraft version"
					placeholder="MC version"
				/>
				{#if kind === 'shaderpack'}
					<select
						class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
						bind:value={searchLoader}
						aria-label="Shader loader"
					>
						<option value="">Any loader</option>
						<option value="iris">Iris</option>
						<option value="optifine">OptiFine</option>
					</select>
				{/if}
				<CarbonButton
					kind="primary"
					size="sm"
					class="h-8 rounded-none"
					onclick={() => void runSearch()}
					disabled={searching}
				>
					{#if searching}
						<Loader2 class="mr-1.5 h-3.5 w-3.5 animate-spin" />
					{:else}
						<Search class="mr-1.5 h-3.5 w-3.5" />
					{/if}
					Search
				</CarbonButton>
			</div>

			{#if searchError}
				<p class="border border-[#da1e28]/50 bg-[#da1e28]/10 p-2 text-xs text-[#ff8389]">
					{searchError}
				</p>
			{/if}

			{#if selectedProject}
				<div class="space-y-2">
					<div class="flex items-center justify-between">
						<div class="flex min-w-0 items-center gap-2">
							{#if selectedProject.icon_url}
								<img
									src={selectedProject.icon_url}
									alt=""
									class="h-8 w-8 border border-[#393939] object-cover"
								/>
							{/if}
							<div class="min-w-0">
								<p class="truncate text-sm font-semibold text-[#f4f4f4]">{selectedProject.title}</p>
								<p class="truncate font-mono text-[10px] text-[#8d8d8d]">
									{selectedProject.author}
								</p>
							</div>
						</div>
						<button
							type="button"
							class="flex items-center gap-1 font-mono text-xs text-[#78a9ff] hover:text-white"
							onclick={() => (selectedProject = null)}
						>
							<X class="h-3.5 w-3.5" /> Back
						</button>
					</div>

					{#if loadingVersions}
						<div class="flex items-center justify-center py-8 text-[#8d8d8d]">
							<Loader2 class="h-5 w-5 animate-spin" />
						</div>
					{:else if versions.length === 0}
						<p class="py-6 text-center text-xs text-[#a8a8a8]">
							No compatible {meta.singular} versions for Minecraft {searchVersion || mcVersion}.
						</p>
					{:else}
						<div class="max-h-56 divide-y divide-[#2a2a2a] overflow-y-auto border border-[#393939]">
							{#each versions as version (version.id)}
								<label
									class="flex cursor-pointer items-center gap-3 px-3 py-2 transition-colors hover:bg-[#262626]"
								>
									<input
										type="radio"
										name="content-version"
										value={version.id}
										checked={selectedVersionId === version.id}
										onchange={() => (selectedVersionId = version.id)}
										class="h-3.5 w-3.5 accent-[#0f62fe]"
									/>
									<span class="min-w-0 flex-1">
										<span class="block truncate text-xs font-medium text-[#f4f4f4]">
											{version.name || version.version_number}
										</span>
										<span class="block truncate font-mono text-[10px] text-[#8d8d8d]">
											{version.file_name} · {formatBytes(version.file_size)}
										</span>
									</span>
									<CarbonTag type="gray" size="sm">{version.version_type}</CarbonTag>
								</label>
							{/each}
						</div>

						<div class="flex flex-wrap items-end gap-3 border-t border-[#393939] pt-3">
							<label
								class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase"
							>
								Side
								<select
									class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4]"
									bind:value={installSide}
								>
									<option value="both">Both</option>
									<option value="client">Client only</option>
									<option value="server">Server only</option>
								</select>
							</label>
							<label class="flex items-center gap-2 pb-1.5 text-xs text-[#c6c6c6]">
								<input
									type="checkbox"
									bind:checked={installOptional}
									class="h-3.5 w-3.5 accent-[#0f62fe]"
								/>
								Optional
							</label>
							<label class="flex items-center gap-2 pb-1.5 text-xs text-[#c6c6c6]">
								<input
									type="checkbox"
									bind:checked={installPinned}
									class="h-3.5 w-3.5 accent-[#0f62fe]"
								/>
								Pin version
							</label>
							<div class="ml-auto flex items-center gap-2">
								<CarbonButton
									kind="secondary"
									size="sm"
									class="rounded-none"
									onclick={() => (addOpen = false)}
								>
									Cancel
								</CarbonButton>
								<CarbonButton
									kind="primary"
									size="sm"
									class="rounded-none"
									onclick={installVersion}
									disabled={installing || !selectedVersionId}
								>
									{#if installing}
										<Loader2 class="mr-1.5 h-3.5 w-3.5 animate-spin" />
									{:else}
										<Plus class="mr-1.5 h-3.5 w-3.5" />
									{/if}
									Install
								</CarbonButton>
							</div>
						</div>
					{/if}
				</div>
			{:else}
				<div class="max-h-72 space-y-1 overflow-y-auto">
					{#if searching && searchResults.length === 0}
						<div class="flex items-center justify-center py-10 text-[#8d8d8d]">
							<Loader2 class="h-5 w-5 animate-spin" />
						</div>
					{:else if !searching && searchResults.length === 0}
						<p class="py-8 text-center text-xs text-[#a8a8a8]">
							Search Modrinth for {meta.title.toLowerCase()} compatible with Minecraft
							{searchVersion || mcVersion}.
						</p>
					{:else}
						{#each searchResults as result (result.id)}
							<button
								type="button"
								class="flex w-full items-start gap-3 rounded-none border border-transparent p-2 text-left transition-colors hover:border-[#393939] hover:bg-[#262626]"
								onclick={() => chooseProject(result)}
							>
								{#if result.icon_url}
									<img
										src={result.icon_url}
										alt=""
										class="h-9 w-9 shrink-0 border border-[#393939] bg-[#161616] object-cover"
									/>
								{:else}
									<div
										class="flex h-9 w-9 shrink-0 items-center justify-center border border-[#393939] bg-[#161616]"
									>
										<Layers class="h-4 w-4 text-[#8d8d8d]" />
									</div>
								{/if}
								<span class="min-w-0 flex-1">
									<span class="flex items-center gap-2">
										<span class="truncate text-sm font-medium text-[#f4f4f4]">{result.title}</span>
										<CarbonTag type="gray" size="sm">{result.author}</CarbonTag>
									</span>
									<span class="line-clamp-2 block text-xs text-[#a8a8a8]">{result.description}</span
									>
								</span>
								<span class="shrink-0 font-mono text-[10px] text-[#8d8d8d]">
									{result.downloads.toLocaleString()} DL
								</span>
							</button>
						{/each}
					{/if}
				</div>
			{/if}
		{:else if addTab === 'url'}
			<div class="space-y-3">
				<p class="text-xs text-[#a8a8a8]">
					The panel fetches the file, computes its SHA-512, and stores a packwiz metafile. Direct
					URLs are not update-tracked.
				</p>
				<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
					Name
					<input
						class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
						bind:value={addForm.name}
						placeholder="Terralith"
					/>
				</label>
				<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
					Download URL
					<input
						class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
						bind:value={addForm.download_url}
						placeholder="https://cdn.example/pack.zip"
						spellcheck="false"
					/>
				</label>
				<div class="grid gap-3 sm:grid-cols-2">
					<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
						File name (optional)
						<input
							class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
							bind:value={addForm.file_name}
							placeholder="Derived from URL"
							spellcheck="false"
						/>
					</label>
					<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
						Hash (optional)
						<div class="flex gap-1">
							<select
								class="h-8 rounded-none border border-[#393939] bg-[#262626] px-1 font-mono text-xs text-[#f4f4f4]"
								bind:value={addForm.hash_format}
							>
								<option value="sha512">sha512</option>
								<option value="sha256">sha256</option>
								<option value="sha1">sha1</option>
								<option value="md5">md5</option>
							</select>
							<input
								class="h-8 min-w-0 flex-1 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
								bind:value={addForm.hash}
								placeholder="Computed if empty"
								spellcheck="false"
							/>
						</div>
					</label>
				</div>
				<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
					Side
					<select
						class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4]"
						bind:value={addForm.side}
					>
						<option value="both">Both</option>
						<option value="client">Client only</option>
						<option value="server">Server only</option>
					</select>
				</label>
				<div class="flex flex-wrap gap-4 border-t border-[#393939] pt-3">
					<label class="flex items-center gap-2 text-xs text-[#c6c6c6]">
						<input
							type="checkbox"
							bind:checked={addForm.pinned}
							class="h-3.5 w-3.5 accent-[#0f62fe]"
						/>
						Pin version
					</label>
					<label class="flex items-center gap-2 text-xs text-[#c6c6c6]">
						<input
							type="checkbox"
							bind:checked={addForm.optional}
							class="h-3.5 w-3.5 accent-[#0f62fe]"
						/>
						Optional
					</label>
					{#if addForm.optional}
						<label class="flex items-center gap-2 text-xs text-[#c6c6c6]">
							<input
								type="checkbox"
								bind:checked={addForm.default}
								class="h-3.5 w-3.5 accent-[#0f62fe]"
							/>
							Enabled by default
						</label>
					{/if}
				</div>
				{#if addForm.optional}
					<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
						Option description
						<input
							class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
							bind:value={addForm.option_description}
							placeholder="Shown when the player chooses optional files"
						/>
					</label>
				{/if}
				<div class="flex justify-end gap-2 border-t border-[#393939] pt-3">
					<CarbonButton
						kind="secondary"
						size="sm"
						class="rounded-none"
						onclick={() => (addOpen = false)}
					>
						Cancel
					</CarbonButton>
					<CarbonButton
						kind="primary"
						size="sm"
						class="rounded-none"
						onclick={addFromURL}
						disabled={installing}
					>
						{#if installing}
							<Loader2 class="mr-1.5 h-3.5 w-3.5 animate-spin" />
						{:else}
							<Link2 class="mr-1.5 h-3.5 w-3.5" />
						{/if}
						Add from URL
					</CarbonButton>
				</div>
			</div>
		{:else}
			<div class="space-y-3">
				<p class="text-xs text-[#a8a8a8]">
					The archive is validated ({kind === 'datapack'
						? 'pack.mcmeta'
						: kind === 'resourcepack'
							? 'pack.mcmeta + assets/'
							: 'shaders/'}) before it is stored. Uploads are served from this panel and carry a
					SHA-512 checksum.
				</p>
				<input
					bind:this={uploadInput}
					type="file"
					accept={meta.accept}
					class="hidden"
					onchange={chooseUpload}
				/>
				<button
					type="button"
					class="flex w-full cursor-pointer flex-col items-center justify-center gap-2 border border-dashed border-[#525252] bg-[#161616] px-4 py-8 transition-colors hover:border-[#0f62fe] hover:bg-[#262626]"
					onclick={() => uploadInput?.click()}
				>
					<Upload class="h-6 w-6 text-[#78a9ff]" />
					<span class="text-sm font-medium text-[#f4f4f4]">
						{uploadTarget ? uploadTarget.name : `Choose a ${meta.singular} .zip`}
					</span>
					<span class="font-mono text-[10px] text-[#8d8d8d]">
						{uploadTarget ? formatBytes(uploadTarget.size) : 'Click to browse'}
					</span>
				</button>
				<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
					Name
					<input
						class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
						bind:value={addForm.name}
						placeholder="Display name"
					/>
				</label>
				<div class="grid gap-3 sm:grid-cols-2">
					<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
						File name
						<input
							class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
							bind:value={addForm.file_name}
							spellcheck="false"
						/>
					</label>
					<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
						Side
						<select
							class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4]"
							bind:value={addForm.side}
						>
							<option value="both">Both</option>
							<option value="client">Client only</option>
							<option value="server">Server only</option>
						</select>
					</label>
				</div>
				<div class="flex flex-wrap gap-4 border-t border-[#393939] pt-3">
					<label class="flex items-center gap-2 text-xs text-[#c6c6c6]">
						<input
							type="checkbox"
							bind:checked={addForm.pinned}
							class="h-3.5 w-3.5 accent-[#0f62fe]"
						/>
						Pin version
					</label>
					<label class="flex items-center gap-2 text-xs text-[#c6c6c6]">
						<input
							type="checkbox"
							bind:checked={addForm.optional}
							class="h-3.5 w-3.5 accent-[#0f62fe]"
						/>
						Optional
					</label>
					{#if addForm.optional}
						<label class="flex items-center gap-2 text-xs text-[#c6c6c6]">
							<input
								type="checkbox"
								bind:checked={addForm.default}
								class="h-3.5 w-3.5 accent-[#0f62fe]"
							/>
							Enabled by default
						</label>
					{/if}
				</div>
				{#if addForm.optional}
					<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
						Option description
						<input
							class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
							bind:value={addForm.option_description}
							placeholder="Shown when the player chooses optional files"
						/>
					</label>
				{/if}
				<div class="flex justify-end gap-2 border-t border-[#393939] pt-3">
					<CarbonButton
						kind="secondary"
						size="sm"
						class="rounded-none"
						onclick={() => (addOpen = false)}
					>
						Cancel
					</CarbonButton>
					<CarbonButton
						kind="primary"
						size="sm"
						class="rounded-none"
						onclick={addFromUpload}
						disabled={uploading || !uploadTarget}
					>
						{#if uploading}
							<Loader2 class="mr-1.5 h-3.5 w-3.5 animate-spin" />
						{:else}
							<Upload class="mr-1.5 h-3.5 w-3.5" />
						{/if}
						Upload {meta.singular}
					</CarbonButton>
				</div>
			</div>
		{/if}
	</div>
</CarbonModal>

<!-- Edit options -->
<CarbonModal
	bind:open={editOpen}
	title="Edit {editForm.name || meta.singular}"
	size="2xl"
	hasFooter={false}
	onclose={() => {
		editOpen = false;
		editTarget = null;
	}}
>
	<div class="space-y-3">
		<p class="text-xs text-[#a8a8a8]">
			Packwiz options apply to every launcher: side controls who receives the file, optional files
			can be deselected, and pinning stops update checks.
		</p>
		<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
			Name
			<input
				class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
				bind:value={editForm.name}
			/>
		</label>
		<div class="grid gap-3 sm:grid-cols-2">
			<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
				File name
				<input
					class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
					bind:value={editForm.file_name}
					spellcheck="false"
				/>
			</label>
			<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
				Side
				<select
					class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4]"
					bind:value={editForm.side}
				>
					<option value="both">Both</option>
					<option value="client">Client only</option>
					<option value="server">Server only</option>
				</select>
			</label>
		</div>

		{#if editTarget?.platform === 'url'}
			<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
				Download URL
				<input
					class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
					bind:value={editForm.download_url}
					spellcheck="false"
				/>
			</label>
			<div class="grid grid-cols-[6rem_minmax(0,1fr)] gap-2">
				<select
					class="h-8 rounded-none border border-[#393939] bg-[#262626] px-1 font-mono text-xs text-[#f4f4f4]"
					bind:value={editForm.hash_format}
					aria-label="Hash format"
				>
					<option value="sha512">sha512</option>
					<option value="sha256">sha256</option>
					<option value="sha1">sha1</option>
					<option value="md5">md5</option>
				</select>
				<input
					class="h-8 min-w-0 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
					bind:value={editForm.hash}
					placeholder="Hash"
					aria-label="Hash"
					spellcheck="false"
				/>
			</div>
		{/if}

		<div class="flex flex-wrap gap-4 border-t border-[#393939] pt-3">
			<label class="flex items-center gap-2 text-xs text-[#c6c6c6]">
				<input
					type="checkbox"
					bind:checked={editForm.pinned}
					class="h-3.5 w-3.5 accent-[#0f62fe]"
				/>
				Pin version
			</label>
			<label class="flex items-center gap-2 text-xs text-[#c6c6c6]">
				<input
					type="checkbox"
					bind:checked={editForm.optional}
					class="h-3.5 w-3.5 accent-[#0f62fe]"
				/>
				Optional
			</label>
			{#if editForm.optional}
				<label class="flex items-center gap-2 text-xs text-[#c6c6c6]">
					<input
						type="checkbox"
						bind:checked={editForm.default}
						class="h-3.5 w-3.5 accent-[#0f62fe]"
					/>
					Enabled by default
				</label>
			{/if}
		</div>
		{#if editForm.optional}
			<label class="flex flex-col gap-1 text-[10px] tracking-wider text-[#8d8d8d] uppercase">
				Option description
				<input
					class="h-8 rounded-none border border-[#393939] bg-[#262626] px-2 font-mono text-xs text-[#f4f4f4] focus:border-[#0f62fe] focus:outline-none"
					bind:value={editForm.option_description}
					placeholder="Shown when the player chooses optional files"
				/>
			</label>
		{/if}

		<div class="flex items-center justify-between border-t border-[#393939] pt-3">
			<span class="font-mono text-[10px] text-[#6f6f6f]">{editTarget?.slug}</span>
			<div class="flex gap-2">
				<CarbonButton
					kind="secondary"
					size="sm"
					class="rounded-none"
					onclick={() => {
						editOpen = false;
						editTarget = null;
					}}
				>
					Cancel
				</CarbonButton>
				<CarbonButton
					kind="primary"
					size="sm"
					class="rounded-none"
					onclick={saveEdit}
					disabled={savingEdit || !editForm.name.trim() || !editForm.file_name.trim()}
				>
					{#if savingEdit}
						<Loader2 class="mr-1.5 h-3.5 w-3.5 animate-spin" />
					{:else}
						<Check class="mr-1.5 h-3.5 w-3.5" />
					{/if}
					Save options
				</CarbonButton>
			</div>
		</div>
	</div>
</CarbonModal>

<CarbonConfirm
	bind:open={deleteOpen}
	title="Remove {deleteTarget?.name ?? meta.singular}?"
	message="Removes the packwiz metadata{deleteTarget?.platform === 'url'
		? ' and the panel-hosted archive'
		: ''}. This does not touch other files in the pack."
	confirmLabel="Remove"
	danger={true}
	confirming={deleting}
	onconfirm={confirmDelete}
	onclose={() => (deleteTarget = null)}
/>
