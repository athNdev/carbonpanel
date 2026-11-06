<!-- SPDX-License-Identifier: MPL-2.0 -->
<script lang="ts">
	import changelog from '$lib/changelog.md?raw';
	import { CarbonPageHeader, CarbonTag } from '$lib/components/carbon';
	import { Check, Plus, RefreshCw } from '@lucide/svelte';
	import { marked } from 'marked';

	// Drop the markdown's leading H1 so it does not duplicate the page title.
	const body = changelog.replace(/^#\s+Changelog\s*$/m, '');

	interface ChangeGroup {
		kind: string;
		html: string;
	}

	interface VersionSection {
		id: string;
		title: string;
		isUnreleased: boolean;
		groups: ChangeGroup[];
	}

	function slugifyVersion(title: string): string {
		return title
			.toLowerCase()
			.replace(/\[|\]/g, '')
			.replace(/[^a-z0-9]+/g, '-')
			.replace(/^-+|-+$/g, '')
			.slice(0, 64);
	}

	function renderMarkdown(src: string): string {
		// The changelog is trusted repository content (not user input), so
		// rendering it as HTML is safe. `async: false` keeps a plain string.
		return marked.parse(src, { async: false }) as string;
	}

	function parseVersions(src: string): { introHtml: string; versions: VersionSection[] } {
		// Link reference definitions (e.g. `[Unreleased]: https://...`) only
		// define URLs; strip them so they never render as visible paragraphs.
		const withoutRefs = src
			.split('\n')
			.filter((line) => !/^\[.+\]:\s*https?:\/\//.test(line.trim()))
			.join('\n');
		const lines = withoutRefs.split('\n');
		const introLines: string[] = [];
		const versions: VersionSection[] = [];
		let currentTitle: string | null = null;
		let currentLines: string[] = [];

		const flush = () => {
			if (currentTitle === null) return;
			const title = currentTitle;
			const sectionBody = currentLines.join('\n').trim();
			const groups: ChangeGroup[] = [];
			// Split `### Added` / `### Fixed` / `### Changed` subsections so
			// each group renders with its own icon. Anything before the first
			// `###` stays as an ungrouped lead block.
			const parts = sectionBody.split(/^###\s+(.+)$/m);
			const lead = (parts[0] ?? '').trim();
			if (lead) groups.push({ kind: '', html: renderMarkdown(lead) });
			for (let i = 1; i < parts.length; i += 2) {
				const kind = (parts[i] ?? '').trim();
				const groupBody = (parts[i + 1] ?? '').trim();
				if (!groupBody) continue;
				groups.push({ kind, html: renderMarkdown(groupBody) });
			}
			versions.push({
				id: `version-${slugifyVersion(title)}`,
				title,
				isUnreleased: /unreleased/i.test(title),
				groups
			});
		};

		for (const line of lines) {
			const match = line.match(/^##\s+(.+)$/);
			if (match) {
				if (currentTitle === null) {
					// First version header ends the intro block.
				} else {
					flush();
				}
				currentTitle = match[1].trim();
				currentLines = [];
			} else if (currentTitle === null) {
				introLines.push(line);
			} else {
				currentLines.push(line);
			}
		}
		flush();
		return { introHtml: renderMarkdown(introLines.join('\n').trim()), versions };
	}

	const { introHtml, versions } = parseVersions(body);
</script>

<div class="space-y-4">
	<CarbonPageHeader title="Changelog" />
	{#if introHtml.trim()}
		<!-- eslint-disable-next-line svelte/no-at-html-tags -- changelog.md is trusted repository content, not user input -->
		<div class="changelog-md changelog-intro">{@html introHtml}</div>
	{/if}
	<div class="grid gap-6 lg:grid-cols-[220px_1fr]">
		<aside class="lg:sticky lg:top-4 lg:self-start">
			<nav aria-label="Versions" class="border border-[#393939] bg-[#262626] p-3">
				<p
					class="px-1 pb-2 font-sans text-xs font-semibold tracking-wider text-[#a8a8a8] uppercase"
				>
					Versions
				</p>
				<ul class="space-y-1">
					{#each versions as version (version.id)}
						<li>
							<a
								href={`#${version.id}`}
								class="block truncate px-2 py-1.5 font-sans text-xs text-[#c6c6c6] transition-colors hover:bg-[#353535] hover:text-white"
							>
								{version.title}
							</a>
						</li>
					{/each}
				</ul>
			</nav>
		</aside>
		<div class="min-w-0 space-y-8">
			{#each versions as version (version.id)}
				<section aria-labelledby={version.id} class="changelog-md">
					<div class="flex flex-wrap items-center gap-2">
						<h2 id={version.id} class="changelog-version-title">{version.title}</h2>
						<CarbonTag type={version.isUnreleased ? 'blue' : 'gray'} size="sm">
							{version.isUnreleased ? 'Unreleased' : 'Release'}
						</CarbonTag>
					</div>
					{#each version.groups as group (group.kind + group.html.slice(0, 32))}
						{#if group.kind}
							<div class="mt-4 flex items-center gap-2">
								{#if group.kind.toLowerCase() === 'added'}
									<Plus class="h-4 w-4 text-[#24a148]" aria-hidden="true" />
								{:else if group.kind.toLowerCase() === 'fixed'}
									<Check class="h-4 w-4 text-[#0f62fe]" aria-hidden="true" />
								{:else}
									<RefreshCw class="h-4 w-4 text-[#8d8d8d]" aria-hidden="true" />
								{/if}
								<h3 class="changelog-group-title">{group.kind}</h3>
							</div>
						{/if}
						<!-- eslint-disable-next-line svelte/no-at-html-tags -- changelog.md is trusted repository content, not user input -->
						<div class="changelog-group-body">{@html group.html}</div>
					{/each}
				</section>
			{/each}
		</div>
	</div>
</div>

<style>
	.changelog-md {
		max-width: 72ch;
		color: #f4f4f4;
		font-size: 0.9375rem;
		line-height: 1.7;
	}
	.changelog-intro {
		max-width: 72ch;
	}
	.changelog-version-title {
		margin: 0;
		font-size: 1.5rem;
		font-weight: 600;
		color: #ffffff;
		border-bottom: 1px solid #393939;
		padding-bottom: 0.5rem;
		scroll-margin-top: 5rem;
		flex: 1 1 auto;
		min-width: 0;
	}
	.changelog-group-title {
		margin: 0;
		font-size: 1.0625rem;
		font-weight: 600;
		color: #c6c6c6;
	}
	.changelog-md :global(h1) {
		margin: 1.5rem 0 0.75rem;
		font-size: 1.5rem;
		font-weight: 600;
		color: #ffffff;
		border-bottom: 1px solid #393939;
		padding-bottom: 0.5rem;
		scroll-margin-top: 5rem;
	}
	.changelog-md :global(h2) {
		margin: 1.5rem 0 0.75rem;
		font-size: 1.5rem;
		font-weight: 600;
		color: #ffffff;
		border-bottom: 1px solid #393939;
		padding-bottom: 0.5rem;
		scroll-margin-top: 5rem;
	}
	.changelog-md :global(h3) {
		margin: 1.25rem 0 0.5rem;
		font-size: 1.0625rem;
		font-weight: 600;
		color: #c6c6c6;
		scroll-margin-top: 5rem;
	}
	.changelog-md :global(ul) {
		margin: 0.5rem 0;
		padding-left: 1.5rem;
		list-style: disc;
	}
	.changelog-md :global(li) {
		margin: 0.5rem 0;
	}
	.changelog-md :global(p) {
		margin: 0.75rem 0;
	}
	.changelog-md :global(a) {
		color: #78a9ff;
		text-decoration: underline;
		text-underline-offset: 2px;
	}
	.changelog-md :global(a:hover) {
		color: #a6c8ff;
	}
	.changelog-md :global(code) {
		font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
		font-size: 0.875em;
		background: #262626;
		border: 1px solid #393939;
		padding: 0.1em 0.35em;
		color: #ff8389;
	}
	.changelog-md :global(pre) {
		background: #262626;
		border: 1px solid #393939;
		padding: 0.75rem 1rem;
		overflow-x: auto;
		margin: 0.75rem 0;
	}
	.changelog-md :global(pre code) {
		background: none;
		border: none;
		padding: 0;
		color: #f4f4f4;
	}
	.changelog-md :global(hr) {
		border: none;
		border-top: 1px solid #393939;
		margin: 1.5rem 0;
	}
	.changelog-md :global(blockquote) {
		border-left: 3px solid #525252;
		margin: 0.75rem 0;
		padding-left: 1rem;
		color: #a8a8a8;
	}
</style>
