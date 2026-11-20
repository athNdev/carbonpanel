// SPDX-License-Identifier: MPL-2.0

import { ModLoader } from '$lib/proto/carbonpanel/v1/common_pb';
import type { GetModVersionsRequest, ModVersion } from '$lib/proto/carbonpanel/v1/mod_browse_pb';

export interface QueuedModFile {
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
}

export function supportsWizardMods(loader: ModLoader): boolean {
	return [ModLoader.FABRIC, ModLoader.FORGE, ModLoader.NEOFORGE, ModLoader.QUILT].includes(loader);
}

type ModTarget = Pick<GetModVersionsRequest, 'platform' | 'loader' | 'mcVersion'>;
type VersionLookup = (target: ModTarget & { project: string }) => Promise<ModVersion[]>;

export async function resolveWizardMod(
	target: ModTarget,
	root: { id: string; name: string; version: ModVersion },
	getVersions: VersionLookup
): Promise<QueuedServerMod> {
	const files = new Map<string, QueuedModFile>();

	async function visit(projectId: string, name: string, version: ModVersion) {
		const project = `${target.platform}:${projectId}`;
		const previous = files.get(project);
		if (previous) {
			if (previous.version !== version.id)
				throw new Error(`${name} requires conflicting versions.`);
			return;
		}
		if (files.size >= 50) throw new Error('This mod requires more than 50 downloads.');
		if (!version.downloadUrl || !/^[^/\\]+\.jar$/i.test(version.fileName)) {
			throw new Error(`${name} has no downloadable JAR for this server.`);
		}
		files.set(project, {
			project,
			version: version.id,
			name,
			fileName: version.fileName,
			downloadUrl: version.downloadUrl
		});
		for (const dependency of version.dependencies) {
			if (dependency.dependencyType.toLowerCase() !== 'required') continue;
			const label = dependency.title || dependency.projectId || dependency.versionId;
			if (!dependency.projectId)
				throw new Error(`Could not identify required dependency ${label}.`);
			const current = files.get(`${target.platform}:${dependency.projectId}`);
			if (current && (!dependency.versionId || current.version === dependency.versionId)) continue;
			const versions = await getVersions({ ...target, project: dependency.projectId });
			const compatible = dependency.versionId
				? versions.find((item) => item.id === dependency.versionId)
				: (versions.find((item) => item.versionType === 'release') ?? versions[0]);
			if (!compatible) throw new Error(`No compatible version of required dependency ${label}.`);
			await visit(dependency.projectId, dependency.title || compatible.name || label, compatible);
		}
	}

	await visit(root.id, root.name, root.version);
	return {
		project: `${target.platform}:${root.id}`,
		name: root.name,
		versionLabel: root.version.versionNumber || root.version.name,
		files: mergeWizardModFiles([...files.values()])
	};
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
