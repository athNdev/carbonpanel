// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Packwiz Studio mod management and catalog workflows: side/pin/remove/batch,
 * online search, version lookup and add-by-URL. The route keeps selection,
 * dialogs, toasts and the dependency sub-workflow; this module owns the exact
 * requests and the domain mapping. Mechanical move only — including the
 * historical `json()`-before-`res.ok` search behavior and the CurseForge
 * id-versus-slug version lookup rule.
 */

/** Transport seam: the panel's real `apiFetch` satisfies this signature. */
export type ApiFetch = (
	input: RequestInfo | URL,
	init?: RequestInit
) => Response | Promise<Response>;

/** A mod entry as stored in a pack. */
export interface ModItem {
	slug: string;
	name: string;
	file_name: string;
	side: 'both' | 'client' | 'server';
	platform: string;
	project_id: string;
	version_id: string;
	download_url: string;
	file_size?: number;
	pinned?: boolean;
	kind?: string;
	optional?: boolean;
	default?: boolean;
	option_description?: string;
	hash?: string;
	hash_format?: string;
	sha512?: string;
}

/** A search result from the online mod catalog. */
export interface SearchModResult {
	id: string;
	slug: string;
	title: string;
	description: string;
	icon_url: string;
	author: string;
	downloads: number;
	categories: string[];
	platform: string;
}

import type { VersionDependency } from './dependencies';

/** A version choice returned by the catalog version lookup. */
export interface ModVersionChoice {
	id: string;
	file_name?: string;
	fileName?: string;
	download_url?: string;
	downloadUrl?: string;
	file_size?: number;
	fileSize?: number;
	dependencies?: VersionDependency[];
}

/** Fields collected by the direct-download URL form. */
export interface UrlModInput {
	name: string;
	fileName: string;
	downloadUrl: string;
	side: 'both' | 'client' | 'server';
	pinned: boolean;
}

/** Batch actions supported by the pack mod endpoint. */
export type BatchModAction = 'set_side' | 'pin' | 'unpin' | 'remove';

/** Rotate a mod side with the historical both -> server -> client -> both order. */
export function nextModSide(side: string): 'both' | 'client' | 'server' {
	const nextSide: Record<string, 'both' | 'client' | 'server'> = {
		both: 'server',
		server: 'client',
		client: 'both'
	};
	return nextSide[side] || 'both';
}

/** PATCH a mod's side and pinned flag. Throws `HTTP <status>` on failure. */
export async function patchMod(
	packId: string,
	slug: string,
	side: ModItem['side'],
	pinned: boolean,
	apiFetch: ApiFetch
): Promise<void> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/mods/${slug}`, {
		method: 'PATCH',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ side, pinned })
	});
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
}

/** DELETE a mod from a pack. Throws `HTTP <status>` on failure. */
export async function removeModFromPack(
	packId: string,
	slug: string,
	apiFetch: ApiFetch
): Promise<void> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/mods/${slug}`, {
		method: 'DELETE'
	});
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
}

/** Apply a batch action to a set of slugs. Throws `HTTP <status>` on failure. */
export async function batchModAction(
	packId: string,
	action: BatchModAction,
	slugs: string[],
	side: string | undefined,
	apiFetch: ApiFetch
): Promise<void> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/mods/batch`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ action, slugs, side })
	});
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
}

/** Result of an online catalog search: either results or a display error. */
export interface ModSearchResult {
	results: SearchModResult[];
	error: string;
}

/**
 * Search the online mod catalog. The JSON body is parsed before `res.ok` is
 * checked, preserving the legacy adapter's error-message behavior; a parse
 * failure is thrown to the route's existing catch.
 */
export async function searchOnlineMods(
	query: string,
	platform: string,
	loader: string,
	mcVersion: string,
	apiFetch: ApiFetch
): Promise<ModSearchResult> {
	const params = new URLSearchParams({
		query: query.trim(),
		platform,
		loader: loader.toLowerCase(),
		mc_version: mcVersion
	});
	const res = await apiFetch(`/api/v1/servers/none/mods/search?${params.toString()}`);
	const data = await res.json();
	if (!res.ok) return { results: [], error: data.error || 'Search request failed' };
	return { results: data.results || [], error: '' };
}

/**
 * Resolve compatible versions for a catalog item. CurseForge looks up by id,
 * other platforms by slug (falling back to id). Throws the same
 * `No compatible versions found` error on a failed lookup.
 */
export async function findModVersions(
	item: SearchModResult,
	loader: string,
	mcVersion: string,
	apiFetch: ApiFetch
): Promise<ModVersionChoice[]> {
	const params = new URLSearchParams({
		platform: item.platform,
		loader: loader.toLowerCase(),
		mc_version: mcVersion
	});
	const modTarget = item.platform === 'curseforge' && item.id ? item.id : item.slug || item.id;
	const res = await apiFetch(
		`/api/v1/servers/none/mods/${modTarget}/versions?${params.toString()}`
	);
	if (!res.ok) throw new Error('No compatible versions found');
	const data = await res.json();
	return data.versions || [];
}

/** Map a catalog item and chosen version into a pack mod entry. */
export function modFromSearch(
	item: SearchModResult,
	selectedVer: ModVersionChoice,
	side: ModItem['side']
): ModItem {
	return {
		slug: item.slug || item.id,
		name: item.title,
		file_name: selectedVer.file_name || selectedVer.fileName || 'mod.jar',
		side,
		platform: item.platform,
		project_id: item.id,
		version_id: selectedVer.id,
		download_url: selectedVer.download_url || selectedVer.downloadUrl || '',
		file_size: selectedVer.file_size || selectedVer.fileSize,
		pinned: false
	};
}

/** POST a new mod into a pack. Throws `HTTP <status>` on failure. */
export async function addModToPack(
	packId: string,
	mod: ModItem,
	apiFetch: ApiFetch
): Promise<void> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/mods`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(mod)
	});
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
}

/** POST a direct-download URL mod and return the created record. */
export async function addUrlMod(
	packId: string,
	input: UrlModInput,
	apiFetch: ApiFetch
): Promise<{ name: string }> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/mods/url`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({
			name: input.name.trim(),
			file_name: input.fileName.trim(),
			download_url: input.downloadUrl.trim(),
			side: input.side,
			pinned: input.pinned
		})
	});
	if (!res.ok) {
		const txt = await res.text();
		throw new Error(txt || `HTTP ${res.status}`);
	}
	return await res.json();
}
