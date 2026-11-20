// SPDX-License-Identifier: MPL-2.0

import { ModLoader } from '$lib/proto/carbonpanel/v1/common_pb';
import type { GetModVersionsRequest, ModVersion } from '$lib/proto/carbonpanel/v1/mod_browse_pb';

export interface WizardModSelection {
	id: string;
	platform: string;
	name: string;
	version: ModVersion;
}

interface QueuedModFile {
	project: string;
	version: string;
	name: string;
	fileName: string;
	downloadUrl: string;
}

export interface QueuedServerMod {
	project: string;
	name: string;
	versionLabel: string;
	files: QueuedModFile[];
	selection: WizardModSelection;
}

export function supportsWizardMods(loader: ModLoader): boolean {
	return [ModLoader.FABRIC, ModLoader.FORGE, ModLoader.NEOFORGE, ModLoader.QUILT].includes(loader);
}

type ModTarget = Pick<GetModVersionsRequest, 'loader' | 'mcVersion'>;
type VersionLookup = (
	target: ModTarget & Pick<GetModVersionsRequest, 'project' | 'platform'>
) => Promise<ModVersion[]>;

interface Requirement {
	id: string;
	platform: string;
	label: string;
	versionId: string;
}

const projectKey = (platform: string, id: string) => `${platform}:${id}`;
const maxDownloads = 50;
const maxAttempts = 250;

export async function resolveWizardMods(
	target: ModTarget,
	roots: WizardModSelection[],
	getVersions: VersionLookup
): Promise<QueuedServerMod[]> {
	const choices = new Map(roots.map((item) => [projectKey(item.platform, item.id), item]));
	const cachedVersions = new Map<string, ModVersion[]>();
	let attempts = 0;

	// Reconsider unpinned choices when a later branch adds an exact constraint.
	// Recompute requirements from the current assignments so rejected candidates
	// cannot leave dependencies or version pins behind.
	async function solve(): Promise<string | undefined> {
		if (++attempts > maxAttempts)
			throw new Error('These mods exceeded the dependency search limit.');
		if (choices.size > maxDownloads) return 'These mods require more than 50 downloads.';
		const pending = new Map<string, Requirement>();
		for (const item of choices.values()) {
			if (!item.version.downloadUrl || !/^[^/\\]+\.jar$/i.test(item.version.fileName)) {
				return `${item.name} has no downloadable JAR for this server.`;
			}
			for (const dep of item.version.dependencies) {
				const kind = dep.dependencyType.toLowerCase();
				if (kind === 'incompatible') {
					const other = dep.projectId
						? choices.get(projectKey(item.platform, dep.projectId))
						: [...choices.values()].find(
								(chosen) => chosen.platform === item.platform && chosen.version.id === dep.versionId
							);
					if (other && (!dep.versionId || other.version.id === dep.versionId)) {
						return `${item.name} is incompatible with ${other.name}.`;
					}
				}
				if (kind !== 'required') continue;
				const label = dep.title || dep.projectId || dep.versionId;
				if (!dep.projectId) return `Could not identify required dependency ${label}.`;
				const key = projectKey(item.platform, dep.projectId);
				const selected = choices.get(key);
				if (selected) {
					if (dep.versionId && selected.version.id !== dep.versionId) {
						return `${selected.name} requires conflicting versions.`;
					}
					continue;
				}
				const existing = pending.get(key);
				if (existing?.versionId && dep.versionId && existing.versionId !== dep.versionId) {
					return `${label} requires conflicting versions.`;
				}
				if (!existing || dep.versionId) {
					pending.set(key, {
						id: dep.projectId,
						platform: item.platform,
						label,
						versionId: dep.versionId
					});
				}
			}
		}
		const next =
			[...pending].find(([, dependency]) => dependency.versionId) ?? pending.entries().next().value;
		if (!next) return;
		const [key, dependency] = next;
		let versions = cachedVersions.get(key);
		if (!versions) {
			if (cachedVersions.size >= maxDownloads)
				throw new Error('These mods exceeded the dependency lookup limit.');
			versions = await getVersions({
				...target,
				platform: dependency.platform,
				project: dependency.id
			});
			cachedVersions.set(key, versions);
		}
		const candidates = dependency.versionId
			? versions.filter((version) => version.id === dependency.versionId)
			: [...versions].sort(
					(a, b) => Number(b.versionType === 'release') - Number(a.versionType === 'release')
				);
		let failure = `No compatible version of required dependency ${dependency.label}.`;
		for (const version of candidates) {
			choices.set(key, {
				id: dependency.id,
				platform: dependency.platform,
				name: version.name || dependency.label,
				version
			});
			const conflict = await solve();
			if (!conflict) return;
			failure = conflict;
			choices.delete(key);
		}
		return failure;
	}

	const failure = await solve();
	if (failure) throw new Error(failure);

	// Keep each root's closure so removing one choice also removes its private
	// dependencies without undoing files still needed by another root.
	const plan = roots.map((selection) => {
		const project = projectKey(selection.platform, selection.id);
		const files = new Map<string, QueuedModFile>();
		function collect(key: string) {
			const item = choices.get(key);
			if (!item || files.has(key)) return;
			files.set(key, {
				project: key,
				version: item.version.id,
				name: item.name,
				fileName: item.version.fileName,
				downloadUrl: item.version.downloadUrl
			});
			for (const dep of item.version.dependencies) {
				if (dep.dependencyType.toLowerCase() === 'required')
					collect(projectKey(item.platform, dep.projectId));
			}
		}
		collect(project);
		return {
			project,
			selection,
			name: selection.name,
			versionLabel: selection.version.versionNumber || selection.version.name,
			files: [...files.values()]
		};
	});
	mergeWizardModFiles(plan.flatMap((mod) => mod.files));
	return plan;
}

export function mergeWizardModFiles(files: QueuedModFile[]): QueuedModFile[] {
	const projects = new Map<string, QueuedModFile>();
	const filenames = new Map<string, QueuedModFile>();
	for (const file of files) {
		const previous = projects.get(file.project);
		if (previous && previous.version !== file.version) {
			throw new Error(
				`${file.name} requires conflicting versions. Remove the other selection first.`
			);
		}
		const filename = file.fileName.toLowerCase();
		const sameName = filenames.get(filename);
		if (sameName && sameName.project !== file.project) {
			throw new Error(`Two mods would install ${file.fileName}. Remove the other selection first.`);
		}
		projects.set(file.project, file);
		filenames.set(filename, file);
	}
	return [...projects.values()];
}
