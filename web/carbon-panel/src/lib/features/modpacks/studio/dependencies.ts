// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Dependency reconciliation for the Packwiz Studio: after a mod is added to a
 * pack, resolve and install its required (transitive) dependencies for the
 * pack's loader/MC version. Optional dependencies are skipped, versions
 * already in the pack are not reinstalled, cycles are guarded by a visited
 * set and the walk is depth-bounded. Unresolvable dependencies are skipped
 * without failing the whole workflow.
 */

/** One dependency entry of a mod version payload (Modrinth/CurseForge shape). */
export interface VersionDependency {
	project_id?: string;
	version_id?: string;
	dependency_type?: string;
}

/** The subset of an installed pack mod that dependency reconciliation reads. */
export interface InstalledPackMod {
	slug: string;
	platform?: string;
	project_id?: string;
}

export type PackModSide = 'both' | 'client' | 'server';

/** Transport seam: the panel's real `apiFetch` satisfies this signature. */
export type ApiFetch = (input: RequestInfo | URL, init?: RequestInit) => Response | Promise<Response>;

export interface DependencyInstallInput {
	/** Direct dependencies of the mod that was just added to the pack. */
	deps: VersionDependency[];
	/** Platform and platform-native id of the added mod. */
	platform: string;
	id: string;
	/** Side that installed dependencies inherit. */
	side: PackModSide;
	/** Pack context for version lookups, duplicate checks and install calls. */
	packId: string;
	modLoader: string;
	mcVersion: string;
	/** Mods already in the pack; reconciliation must not reinstall them. */
	existingMods: InstalledPackMod[];
}

/**
 * Installs the required (transitive) dependencies of a mod that was just
 * added to a pack and returns how many dependencies were added.
 */
export async function installRequiredDependencies(
	input: DependencyInstallInput,
	apiFetch: ApiFetch
): Promise<number> {
	// Seed the visited set from the pack so installed mods (by platform id or
	// slug) are never reinstalled, then visit the added mod's own subtree.
	const visited = new Set<string>();
	for (const m of input.existingMods || []) {
		if (m.project_id)
			visited.add(`${(m.platform || input.platform).toLowerCase()}:${m.project_id.toLowerCase()}`);
		if (m.slug) visited.add(`slug:${m.slug.toLowerCase()}`);
	}
	visited.add(`${input.platform.toLowerCase()}:${input.id.toLowerCase()}`);
	return resolveAndAddDependencies(input, input.deps, 0, visited, apiFetch);
}

// Recursively installs required (transitive) dependencies of an added mod
// for the pack's loader/MC version. Skips optional deps, versions already
// in the pack, and cycles via the visited set. Returns the count added.
async function resolveAndAddDependencies(
	input: DependencyInstallInput,
	deps: VersionDependency[],
	depth: number,
	visited: Set<string>,
	apiFetch: ApiFetch
): Promise<number> {
	if (depth > 3) return 0;
	let added = 0;
	const required = (deps || []).filter(
		(d) => (d.dependency_type || 'required').toLowerCase() === 'required' && d.project_id
	);
	for (const dep of required) {
		const depId = dep.project_id as string;
		const key = `${input.platform.toLowerCase()}:${depId.toLowerCase()}`;
		if (visited.has(key)) continue;
		visited.add(key);
		if (
			(input.existingMods || []).some(
				(m) => m.project_id === depId || m.slug === depId.toLowerCase()
			)
		) {
			continue;
		}
		try {
			const vParams = new URLSearchParams({
				platform: input.platform,
				loader: input.modLoader.toLowerCase(),
				mc_version: input.mcVersion
			});
			const vRes = await apiFetch(
				`/api/v1/servers/none/mods/${depId}/versions?${vParams.toString()}`
			);
			if (!vRes.ok) continue;
			const vData = await vRes.json();
			const versions = vData.versions || [];
			if (versions.length === 0) continue;
			const ver = versions[0];
			const depSlug = depId.toLowerCase();
			if (visited.has(`slug:${depSlug}`)) continue;
			visited.add(`slug:${depSlug}`);
			const depMod = {
				slug: depSlug,
				name: ver.name || ver.version_number || depSlug,
				file_name: ver.file_name || ver.fileName || 'mod.jar',
				side: input.side,
				platform: input.platform,
				project_id: depId,
				version_id: ver.id,
				download_url: ver.download_url || ver.downloadUrl || '',
				file_size: ver.file_size || ver.fileSize,
				pinned: false
			};
			const addRes = await apiFetch(`/api/v1/packwiz/packs/${input.packId}/mods`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(depMod)
			});
			if (!addRes.ok) continue;
			added++;
			added += await resolveAndAddDependencies(
				input,
				ver.dependencies || [],
				depth + 1,
				visited,
				apiFetch
			);
		} catch (err) {
			console.warn(`Skipping unresolvable dependency ${depId}:`, err);
		}
	}
	return added;
}
