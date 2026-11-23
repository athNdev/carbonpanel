// SPDX-License-Identifier: MPL-2.0

// Dependency resolution for a mods-browser install. Extracted from
// mod-browser-dialog.svelte so the outcome is a value, not a side effect:
//
// - Edge case E9: a dependency whose versions cannot be resolved (provider
//   outage, no compatible file) is collected into `unresolved` and surfaced —
//   it is never silently dropped from the install.
// - Edge case E8: transitive required dependencies discovered during planning
//   are folded into the returned `selection` and listed in `autoIncluded`, so
//   the checkbox UI can reflect what will actually be installed.

export interface PlanDependency {
	project_id: string;
	version_id?: string;
	dependency_type: string;
	title?: string;
}

export interface PlanVersion {
	id: string;
	version_number: string;
	name: string;
	version_type: string;
	file_name: string;
	download_url: string;
	file_size: number;
	dependencies: PlanDependency[];
}

export interface DependencyLookupRequest {
	project: string;
	platform: string;
	loader: string;
	mcVersion: string;
}

export type DependencyLookup = (
	request: DependencyLookupRequest
) => Promise<{ versions: PlanVersion[] }>;

export interface DependencyPlanRequest {
	deps: PlanDependency[];
	platform: string;
	loader: string;
	mcVersion: string;
	/** Which dependencies the operator checked (project_id -> checked). */
	selection: Record<string, boolean>;
	/**
	 * Projects already visited above this plan (the root mod, previous runs).
	 * Matching projects are skipped instead of resolved twice.
	 */
	isKnownProject?: (projectId: string) => boolean;
	getVersions: DependencyLookup;
	maxDepth?: number;
}

export interface UnresolvedDependency {
	projectId: string;
	title: string;
	reason: string;
}

export interface InstallItem {
	name: string;
	file_name: string;
	download_url: string;
}

export interface DependencyPlanResult {
	items: InstallItem[];
	unresolved: UnresolvedDependency[];
	/** Transitive required dependencies added to the selection during planning. */
	autoIncluded: string[];
	/** The effective selection including auto-added transitive requirements. */
	selection: Record<string, boolean>;
}

/** Mirrors the dialog's historical depth bound for transitive resolution. */
export const MAX_DEPENDENCY_DEPTH = 3;

const dependencyType = (dep: PlanDependency) => (dep.dependency_type || '').toLowerCase();

export async function buildDependencyPlan(
	request: DependencyPlanRequest
): Promise<DependencyPlanResult> {
	const maxDepth = request.maxDepth ?? MAX_DEPENDENCY_DEPTH;
	const selection = { ...request.selection };
	const visited = new Set<string>();
	const unresolved: UnresolvedDependency[] = [];
	const autoIncluded: string[] = [];
	const items: InstallItem[] = [];

	const projectKey = (platform: string, id: string) =>
		`${platform.toLowerCase()}:${id.toLowerCase()}`;

	async function visit(
		deps: PlanDependency[],
		platform: string,
		loader: string,
		mcVersion: string,
		depth: number
	): Promise<void> {
		if (depth > maxDepth) return;
		const wanted = (deps || []).filter(
			(dep) =>
				dep.project_id &&
				dependencyType(dep) !== 'incompatible' &&
				selection[dep.project_id] === true
		);
		for (const dep of wanted) {
			const depId = dep.project_id as string;
			const key = projectKey(platform, depId);
			if (visited.has(key)) continue;
			if (request.isKnownProject?.(depId)) {
				visited.add(key);
				continue;
			}
			visited.add(key);
			const label = dep.title || depId;
			try {
				const response = await request.getVersions({
					project: depId,
					platform,
					loader,
					mcVersion
				});
				const versions = response.versions;
				if (versions.length === 0) {
					unresolved.push({ projectId: depId, title: label, reason: 'No compatible version' });
					continue;
				}
				const version = versions[0];
				if (!version.file_name || !version.download_url) {
					unresolved.push({
						projectId: depId,
						title: label,
						reason: 'The provider returned no downloadable file'
					});
					continue;
				}
				// Fold this dependency's own required deps into the selection so
				// transitive requirements ride along in the same install (E8).
				for (const trans of version.dependencies || []) {
					if (!trans.project_id) continue;
					if (dependencyType(trans) !== 'required') continue;
					const transKey = projectKey(platform, trans.project_id);
					if (visited.has(transKey) || selection[trans.project_id] !== undefined) continue;
					selection[trans.project_id] = true;
					autoIncluded.push(trans.title || trans.project_id);
				}
				items.push({
					name: version.name || version.version_number || label,
					file_name: version.file_name,
					download_url: version.download_url
				});
				await visit(version.dependencies || [], platform, loader, mcVersion, depth + 1);
			} catch (error) {
				// E9: never silently drop. The reason reaches the operator.
				unresolved.push({
					projectId: depId,
					title: label,
					reason:
						error instanceof Error && error.message
							? `Unresolved: ${error.message}`
							: 'Unresolved: the provider request failed'
				});
			}
		}
	}

	await visit(request.deps, request.platform, request.loader, request.mcVersion, 0);
	return { items, unresolved, autoIncluded, selection };
}
