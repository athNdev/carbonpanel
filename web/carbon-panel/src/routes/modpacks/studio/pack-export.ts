// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

export type PackExportFormat = 'mrpack' | 'curseforge' | 'packwiz' | 'mmc';

export interface PackExportOptions {
	includeClient: boolean;
	optionalMode: 'keep' | 'required' | 'optional';
	overrides: 'all' | 'none' | 'configs-only';
	summary: string;
}

export const DEFAULT_EXPORT_OPTIONS: PackExportOptions = {
	includeClient: true,
	optionalMode: 'keep',
	overrides: 'all',
	summary: ''
};

export const EXPORT_FORMATS: { id: PackExportFormat; label: string; hint: string }[] = [
	{ id: 'mrpack', label: 'Modrinth .mrpack', hint: 'Prism, ATLauncher, MultiMC, Modrinth App' },
	{ id: 'curseforge', label: 'CurseForge .zip', hint: 'Prism, ATLauncher, MultiMC via manifest' },
	{ id: 'mmc', label: 'Prism/MultiMC instance.zip', hint: 'mmc-pack.json + instance.cfg' },
	{ id: 'packwiz', label: 'Packwiz .zip', hint: 'Native pack.toml + index.toml archive' }
];

export function buildExportQuery(opts: PackExportOptions): string {
	const params = new URLSearchParams();
	params.set('include_client', opts.includeClient ? 'true' : 'false');
	params.set('optional_mode', opts.optionalMode);
	params.set('overrides', opts.overrides);
	if (opts.summary.trim()) params.set('summary', opts.summary.trim());
	return params.toString();
}

export function getExportFilename(packName: string, format: PackExportFormat): string {
	const safe = (packName || 'modpack').trim() || 'modpack';
	switch (format) {
		case 'mrpack':
			return `${safe}.mrpack`;
		case 'packwiz':
			return `${safe}.packwiz.zip`;
		case 'mmc':
			return `${safe}-mmc.zip`;
		default:
			return `${safe}.zip`;
	}
}

export function formatExportLabel(format: PackExportFormat): string {
	switch (format) {
		case 'mrpack':
			return '.mrpack';
		case 'packwiz':
			return 'Packwiz .zip';
		case 'mmc':
			return 'instance.zip';
		default:
			return 'CurseForge .zip';
	}
}

/** Mods that CurseForge export cannot carry (non-CurseForge platform). */
export function findCurseForgeDroppedMods(
	mods: { name: string; platform: string; project_id?: string; version_id?: string }[]
): string[] {
	return (mods || [])
		.filter((m) => {
			const platform = (m.platform || '').toLowerCase();
			if (platform !== 'curseforge') return true;
			return !/^\d+$/.test(m.project_id || '') || !/^\d+$/.test(m.version_id || '');
		})
		.map((m) => m.name);
}

export async function downloadPackExport(
	packId: string,
	packName: string,
	format: PackExportFormat,
	opts: PackExportOptions = DEFAULT_EXPORT_OPTIONS
): Promise<{ ok: true } | { ok: false; dropped?: string[]; message: string }> {
	// Dynamic import keeps this shared helper loadable in bun/node tests,
	// which cannot resolve SvelteKit $app aliases pulled in via auth stores.
	const { apiFetch } = await import('$lib/api/fetch');
	const query = buildExportQuery(opts);
	const res = await apiFetch(`/api/v1/packwiz/packs/${packId}/export/${format}?${query}`);
	if (!res.ok) {
		if (format === 'curseforge' && res.status === 422) {
			try {
				const data = await res.json();
				const dropped: string[] = data.dropped || [];
				return {
					ok: false,
					dropped,
					message:
						data.error ||
						`CurseForge export blocked: ${dropped.length} mod(s) have no CurseForge project/file ID.`
				};
			} catch {
				return { ok: false, message: `Export failed: HTTP ${res.status}` };
			}
		}
		const txt = await res.text().catch(() => '');
		return { ok: false, message: txt || `Export failed: HTTP ${res.status}` };
	}
	const blob = await res.blob();
	const url = window.URL.createObjectURL(blob);
	const a = document.createElement('a');
	a.href = url;
	a.download = getExportFilename(packName, format);
	document.body.appendChild(a);
	a.click();
	window.URL.revokeObjectURL(url);
	document.body.removeChild(a);
	return { ok: true };
}
