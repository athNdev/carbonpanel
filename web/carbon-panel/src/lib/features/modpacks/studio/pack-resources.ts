// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Packwiz Studio content-file workflows: override files and the raw pack.toml.
 * The route owns dialogs, flags and toasts; this module owns the exact URLs,
 * payloads and the byte-for-byte text/list results. Mechanical move only — no
 * stricter validation or new error semantics are introduced.
 */

/** Transport seam: the panel's real `apiFetch` satisfies this signature. */
export type ApiFetch = (
	input: RequestInfo | URL,
	init?: RequestInit
) => Response | Promise<Response>;

/** A file listed under a pack's overrides. */
export interface PackFileInfo {
	path: string;
	size: number;
	mod_time: string;
	category: string;
}

/** GET the override files of a pack. Throws `HTTP <status>` on a failed response. */
export async function fetchPackFiles(packId: string, apiFetch: ApiFetch): Promise<PackFileInfo[]> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/files`);
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
	const data = await res.json();
	return data.files || [];
}

/** POST an override file. Throws `HTTP <status>` on a failed response. */
export async function savePackFile(
	packId: string,
	path: string,
	content: string,
	apiFetch: ApiFetch
): Promise<void> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/files`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ path, content })
	});
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
}

/** DELETE an override file. Throws `HTTP <status>` on a failed response. */
export async function deletePackFile(
	packId: string,
	filePath: string,
	apiFetch: ApiFetch
): Promise<void> {
	const res = await apiFetch(
		`/api/v1/packwiz/packs/${packId}/files?path=${encodeURIComponent(filePath)}`,
		{ method: 'DELETE' }
	);
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
}

/**
 * GET the raw pack.toml text. Returns `null` when the response is not ok or
 * the request fails; the previous route behavior kept the last good text in
 * that case. Note the intentionally different `/api/v1/packwiz/{id}/pack.toml`
 * path (no `/packs/` segment).
 */
export async function fetchPackToml(packId: string, apiFetch: ApiFetch): Promise<string | null> {
	try {
		const res = await apiFetch(`/api/v1/packwiz/${packId}/pack.toml`);
		if (res.ok) return await res.text();
	} catch (err) {
		console.error('Failed to load raw tomls:', err);
	}
	return null;
}

/** Install an indexed datapack into a pack. Throws on a failed response. */
export async function installDatapack(
	packId: string,
	datapackId: string,
	gameVersion: string,
	indexer: string,
	apiFetch: ApiFetch
): Promise<void> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/datapacks`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ datapack_id: datapackId, game_version: gameVersion, indexer })
	});
	if (!res.ok) {
		const text = await res.text();
		throw new Error(text || `HTTP ${res.status}`);
	}
}
