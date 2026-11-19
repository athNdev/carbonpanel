// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Packwiz Studio metadata/content workflow: read a project, persist its
 * metadata/content through the same whole-pack PUT, characterise an MC-version
 * change and compute the content breakdown shown on the Studio export tab.
 *
 * This is a mechanical extraction. The GET/PUT URLs, methods, headers and body
 * are byte-for-byte the ones the route used before; the route keeps its form
 * state, migration routing, dialogs and toasts. Template defaults and instance
 * overrides are not merged here — this module only moves the Studio project
 * metadata/content seam.
 */

/** Transport seam: the panel's real `apiFetch` satisfies this signature. */
export type ApiFetch = (
	input: RequestInfo | URL,
	init?: RequestInit
) => Response | Promise<Response>;

/** A mod entry as stored in a Studio project (content of the pack). */
export interface StudioPackMod {
	slug: string;
	name: string;
	file_name: string;
	side: 'both' | 'client' | 'server';
	platform: string;
	project_id: string;
	version_id: string;
	download_url: string;
	pinned?: boolean;
	optional?: boolean;
	[k: string]: unknown;
}

/** The metadata/content record returned by the packwiz project endpoint. */
export interface StudioPack {
	id: string;
	name: string;
	author: string;
	version: string;
	mc_version: string;
	mod_loader: string;
	loader_version: string;
	updated_at: string;
	mods: StudioPackMod[];
	datapacks?: StudioPackMod[];
	resourcepacks?: StudioPackMod[];
	shaderpacks?: StudioPackMod[];
	datapack_folder?: string;
	[k: string]: unknown;
}

/**
 * GET a Studio project. A missing `mods` array is normalised to `[]` exactly as
 * the route did; a falsy response body is returned unchanged. Throws
 * `HTTP <status>` on a non-OK response.
 */
export async function fetchStudioPack(
	packId: string,
	apiFetch: ApiFetch
): Promise<StudioPack | null> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}`);
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
	const data = (await res.json()) as StudioPack | null;
	if (data) {
		data.mods = data.mods || [];
	}
	return data;
}

/**
 * True only when a previous MC version is known and differs. An empty baseline
 * (`loadedMCVersion === ''`) never marks a change, preserving the route's
 * first-load behavior.
 */
export function isMcVersionChanged(loadedMCVersion: string, currentVersion: string): boolean {
	return loadedMCVersion !== '' && currentVersion !== loadedMCVersion;
}

/**
 * All content items exported by the Studio detail dialog, in the historical
 * order: mods, datapacks, resourcepacks, then shaderpacks. Missing arrays are
 * treated as empty, exactly as the route's spread did.
 */
export function allPackContentMods(pack: StudioPack): StudioPackMod[] {
	return [
		...(pack.mods ?? []),
		...(pack.datapacks ?? []),
		...(pack.resourcepacks ?? []),
		...(pack.shaderpacks ?? [])
	];
}

/**
 * PUT the whole project record. The body is the untouched pack object, so
 * metadata and content (mods/packs) stay in one request exactly as before.
 * Throws `HTTP <status>` on failure.
 */
export async function saveStudioPack(
	packId: string,
	pack: StudioPack,
	apiFetch: ApiFetch
): Promise<void> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}`, {
		method: 'PUT',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(pack)
	});
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
}

/** Counts derived from the loaded pack for the export-tab manifest breakdown. */
export interface PackContentBreakdown {
	total: number;
	both: number;
	client: number;
	server: number;
	pinned: number;
	clientOnly: number;
	curseforgeReady: number;
	overrides: number;
}

/**
 * Compute the content breakdown without touching the network. Counts mirror the
 * export tab's historical filters, including the numeric project/file-id rule
 * for CurseForge readiness.
 */
export function summarizePackContent(
	mods: StudioPackMod[],
	overrideCount: number
): PackContentBreakdown {
	const all = mods || [];
	return {
		total: all.length,
		both: all.filter((m) => m.side === 'both').length,
		client: all.filter((m) => m.side === 'client').length,
		server: all.filter((m) => m.side === 'server').length,
		pinned: all.filter((m) => m.pinned).length,
		clientOnly: all.filter((m) => m.side === 'client').length,
		curseforgeReady: all.filter(
			(m) => m.platform === 'curseforge' && /^\d+$/.test(m.project_id || '')
		).length,
		overrides: overrideCount
	};
}
