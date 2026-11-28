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

export interface ModInstallContext {
	serverId: string;
	serverMcVersion: string;
	serverLoader: string | number;
	lookupLoader: string;
	lookupMcVersion: string;
}

export function sameModInstallContext(left: ModInstallContext, right: ModInstallContext): boolean {
	return (
		left.serverId === right.serverId &&
		left.serverMcVersion === right.serverMcVersion &&
		left.serverLoader === right.serverLoader &&
		left.lookupLoader === right.lookupLoader &&
		left.lookupMcVersion === right.lookupMcVersion
	);
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

// Production protos send an empty string for "no pin"; a whitespace-only value
// is the same intent and must not participate in pin conflict detection.
const normalizePin = (pin: string | undefined): string | undefined => {
	const trimmed = pin?.trim();
	return trimmed ? trimmed : undefined;
};

export async function buildDependencyPlan(
	request: DependencyPlanRequest
): Promise<DependencyPlanResult> {
	const maxDepth = request.maxDepth ?? MAX_DEPENDENCY_DEPTH;
	const selection = { ...request.selection };
	const visited = new Set<string>();
	const unresolved: UnresolvedDependency[] = [];
	const depthLimited = new Map<string, UnresolvedDependency>();
	const autoIncluded: string[] = [];
	const items: InstallItem[] = [];

	const projectKey = (platform: string, id: string) =>
		`${platform.toLowerCase()}:${id.toLowerCase()}`;

	// Dedupe is by project, but two requests for the same project can pin
	// different versions. Track the pin each project was planned with so a
	// conflicting request is surfaced instead of silently reusing the first
	// pin (or silently dropping the second one).
	const plannedPins = new Map<string, string | undefined>();
	const reportedConflicts = new Set<string>();
	const pinLabel = (pin: string | undefined) => (pin ? `version ${pin}` : 'no pinned version');

	async function visit(
		deps: PlanDependency[],
		platform: string,
		loader: string,
		mcVersion: string,
		depth: number
	): Promise<void> {
		const wanted = (deps || []).filter(
			(dep) =>
				dep.project_id &&
				dependencyType(dep) !== 'incompatible' &&
				selection[dep.project_id] === true
		);
		if (depth > maxDepth) {
			for (const dep of wanted) {
				const depId = dep.project_id as string;
				const key = projectKey(platform, depId);
				if (visited.has(key)) continue;
				depthLimited.set(key, {
					projectId: depId,
					title: dep.title || depId,
					reason: 'Dependency resolution depth limit reached'
				});
			}
			return;
		}
		for (const dep of wanted) {
			const depId = dep.project_id as string;
			const key = projectKey(platform, depId);
			const pin = normalizePin(dep.version_id);
			if (visited.has(key)) {
				const plannedPin = plannedPins.get(key);
				if (plannedPin !== pin) {
					// A repeated conflicting request must not add a row per
					// duplicate occurrence.
					const conflictKey = `${key}\u0000${plannedPin ?? ''}\u0000${pin ?? ''}`;
					if (!reportedConflicts.has(conflictKey)) {
						reportedConflicts.add(conflictKey);
						unresolved.push({
							projectId: depId,
							title: dep.title || depId,
							reason: `Conflicting pinned versions requested for this project: already planned ${pinLabel(plannedPin)}, also requested ${pinLabel(pin)}`
						});
					}
				}
				continue;
			}
			if (request.isKnownProject?.(depId)) {
				// Known projects are not resolved again, but their pin still
				// owns this project so a later conflicting request is surfaced.
				visited.add(key);
				plannedPins.set(key, pin);
				continue;
			}
			visited.add(key);
			plannedPins.set(key, pin);
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
				const version = pin ? versions.find((candidate) => candidate.id === pin) : versions[0];
				if (!version) {
					unresolved.push({
						projectId: depId,
						title: label,
						reason: `Pinned version ${pin} is not compatible`
					});
					continue;
				}
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
	for (const [key, dependency] of depthLimited) {
		if (!visited.has(key)) unresolved.push(dependency);
	}
	return { items, unresolved, autoIncluded, selection };
}
