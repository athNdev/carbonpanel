// SPDX-License-Identifier: MPL-2.0

// Pure helpers for the installed-mod review and policy flows. Keeping the
// selection and policy construction here makes the operator-visible contract
// testable without mounting the whole Mods tab.

export interface UpdatePreviewLike {
	state?: string;
	installedSha512?: string;
	candidateVersionId?: string;
	candidateSha512?: string;
	candidateFilename?: string;
}

export interface ModLike {
	id: string;
	fileName: string;
	updatePreview?: UpdatePreviewLike | null;
}

export interface UpdateSelection {
	modId: string;
	fileName: string;
	installedSha512: string;
	candidateVersionId: string;
	candidateSha512: string;
	candidateFilename: string;
}

export interface PolicyForm {
	enabled: boolean;
	excludedProjects: string;
	maxPerRun: number;
	skipWhenRunning: boolean;
}

export function updateStateClass(state: string | undefined): string {
	switch (state) {
		case 'available':
			return 'border-success bg-success/10 text-success-text';
		case 'current':
			return 'border-border bg-card text-muted-foreground';
		case 'unknown_source':
		case 'unavailable':
			return 'border-destructive bg-destructive/10 text-destructive-text';
		default:
			return 'border-border bg-card text-foreground';
	}
}

export function buildUpdateSelections(mods: ModLike[], selectedIds: string[]): UpdateSelection[] {
	return mods
		.filter((mod) => selectedIds.includes(mod.id) && mod.updatePreview?.state === 'available')
		.map((mod) => ({
			modId: mod.id,
			fileName: mod.fileName,
			installedSha512: mod.updatePreview!.installedSha512 ?? '',
			candidateVersionId: mod.updatePreview!.candidateVersionId ?? '',
			candidateSha512: mod.updatePreview!.candidateSha512 ?? '',
			candidateFilename: mod.updatePreview!.candidateFilename ?? ''
		}));
}

export function buildPolicyConfig(form: PolicyForm): string {
	return JSON.stringify({
		enabled: form.enabled,
		excluded_projects: form.excludedProjects
			.split(',')
			.map((item) => item.trim())
			.filter(Boolean),
		max_updates_per_run: form.maxPerRun,
		skip_when_running: form.skipWhenRunning,
		window: 'next_restart'
	});
}
