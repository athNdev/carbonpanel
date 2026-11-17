// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Packwiz Studio version-migration workflow: simulate or apply a move of a
 * pack to a target Minecraft version and mod loader. The route owns the form
 * state, toasts and the post-apply reload; this module owns the exact request
 * and the reported compatibility outcome. This is a mechanical move of the
 * existing behavior — no stricter validation is introduced.
 */

/** One mod's compatibility outcome in a migration report. */
export interface MigrationMod {
	slug: string;
	name: string;
	platform: string;
	current_version_id: string;
	target_version_id?: string;
	target_file_name?: string;
	compatible: boolean;
}

/** Compatibility report returned by the migration simulation/apply endpoint. */
export interface MigrationReport {
	pack_id: string;
	current_mc: string;
	current_loader: string;
	target_mc: string;
	target_loader: string;
	total_mods: number;
	compatible_count: number;
	incompatible_count: number;
	mods: MigrationMod[];
}

/** Pack context and target versions for a migration run. */
export interface MigrationRequest {
	packId: string;
	targetMc: string;
	targetLoader: string;
}

/** Transport seam: the panel's real `apiFetch` satisfies this signature. */
export type ApiFetch = (
	input: RequestInfo | URL,
	init?: RequestInit
) => Response | Promise<Response>;

/** Fallback message used by the route when a migration failure has no message. */
export const MIGRATION_FAILURE_MESSAGE = 'Version migration simulation failed';

/**
 * Runs a migration simulation (`apply = false`) or applies it (`apply = true`)
 * and returns the compatibility report. Non-successful responses throw with the
 * same `HTTP <status>` message the route previously produced.
 */
export async function runMigration(
	request: MigrationRequest,
	apply: boolean,
	apiFetch: ApiFetch
): Promise<MigrationReport> {
	const res = await apiFetch(`/api/v1/packwiz/packs/${request.packId}/migrate`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({
			target_mc: request.targetMc,
			target_loader: request.targetLoader,
			apply
		})
	});
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
	return (await res.json()) as MigrationReport;
}

/** Resolves the error text the route shows for a failed migration run. */
export function migrationErrorMessage(err: unknown): string {
	return (err instanceof Error ? err.message : undefined) || MIGRATION_FAILURE_MESSAGE;
}
