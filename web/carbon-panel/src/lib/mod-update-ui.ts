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
	window?: 'next_restart' | 'idle';
	idleMinutes?: number;
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

// Identity of the candidate the operator actually reviewed: the offered row
// (version id, SHA-512 and filename) together with the installed bytes. A
// refresh that changes either half requires a fresh review; the RPC refuses
// installation drift, but it must not be staged under the earlier review.
export function updateCandidateFingerprint(preview?: UpdatePreviewLike | null): string {
	if (!preview) return '';
	return [
		preview.candidateVersionId ?? '',
		preview.candidateSha512 ?? '',
		preview.candidateFilename ?? '',
		preview.installedSha512 ?? ''
	].join('\u0000');
}

// A selection survives a refresh only while the row is still available and the
// currently offered candidate is the one that was reviewed.
export function currentUpdateSelectionIds(
	mods: ModLike[],
	selectedIds: string[],
	selectedFingerprints: Record<string, string>
): string[] {
	const byId = new Map(mods.map((mod) => [mod.id, mod]));
	return selectedIds.filter((id) => {
		const recorded = selectedFingerprints[id];
		if (recorded === undefined) return false;
		const mod = byId.get(id);
		return (
			mod?.updatePreview?.state === 'available' &&
			updateCandidateFingerprint(mod.updatePreview) === recorded
		);
	});
}

export function buildUpdateSelections(
	mods: ModLike[],
	selectedIds: string[],
	selectedFingerprints: Record<string, string>
): UpdateSelection[] {
	return mods
		.filter((mod) => {
			if (!selectedIds.includes(mod.id) || mod.updatePreview?.state !== 'available') return false;
			const recorded = selectedFingerprints[mod.id];
			return recorded !== undefined && updateCandidateFingerprint(mod.updatePreview) === recorded;
		})
		.map((mod) => ({
			modId: mod.id,
			fileName: mod.fileName,
			installedSha512: mod.updatePreview!.installedSha512 ?? '',
			candidateVersionId: mod.updatePreview!.candidateVersionId ?? '',
			candidateSha512: mod.updatePreview!.candidateSha512 ?? '',
			candidateFilename: mod.updatePreview!.candidateFilename ?? ''
		}));
}

export interface UpdateSelectionPrune {
	kept: string[];
	fingerprints: Record<string, string>;
	/** Rows whose offered release or installed bytes changed since review. */
	changed: string[];
	/** Rows that are no longer listed, or were never fingerprinted. */
	missing: string[];
}

// Reconcile a selection with a fresh listing. Only a check supplies previews;
// a plain reload must not read their absence as "every candidate changed". With
// previews, a still-available row whose identity changed is cleared, a row that
// is merely no longer available is retained (it cannot stage until a later
// check offers the reviewed candidate again), and a vanished row is dropped
// silently. Without previews only vanished rows are dropped and the recorded
// fingerprints are kept for the next check.
export function pruneUpdateSelections(
	mods: ModLike[],
	selectedIds: string[],
	selectedFingerprints: Record<string, string>,
	previewsSupplied: boolean
): UpdateSelectionPrune {
	const byId = new Map(mods.map((mod) => [mod.id, mod]));
	const kept: string[] = [];
	const fingerprints: Record<string, string> = {};
	const changed: string[] = [];
	const missing: string[] = [];
	for (const id of selectedIds) {
		const recorded = selectedFingerprints[id];
		const mod = byId.get(id);
		if (!mod || recorded === undefined) {
			missing.push(id);
			continue;
		}
		if (
			previewsSupplied &&
			mod.updatePreview?.state === 'available' &&
			updateCandidateFingerprint(mod.updatePreview) !== recorded
		) {
			changed.push(id);
			continue;
		}
		kept.push(id);
		fingerprints[id] = recorded;
	}
	return { kept, fingerprints, changed, missing };
}

export interface PolicyFormState {
	enabled: boolean;
	cron: string;
	timezone: string;
	excludedProjects: string;
	maxPerRun: number;
	skipWhenRunning: boolean;
	window: 'next_restart' | 'idle';
	idleMinutes: number;
}

export const DEFAULT_POLICY_STATE: PolicyFormState = {
	enabled: false,
	cron: '0 3 * * *',
	timezone: 'UTC',
	excludedProjects: '',
	maxPerRun: 5,
	skipWhenRunning: true,
	window: 'next_restart',
	idleMinutes: 15
};

// The persisted config is the opt-in authority; task status is only a fallback
// for a legacy task without the new config shape.
export function policyFromPersistedTask(
	task: { config?: string; status?: number; cronExpr?: string; timezone?: string } | null,
	enabledStatus: number
): PolicyFormState {
	if (!task) return { ...DEFAULT_POLICY_STATE };
	try {
		const config = JSON.parse(task.config || '{}');
		return {
			enabled: config.enabled === true,
			cron: task.cronExpr || DEFAULT_POLICY_STATE.cron,
			timezone: task.timezone || DEFAULT_POLICY_STATE.timezone,
			excludedProjects: Array.isArray(config.excluded_projects)
				? config.excluded_projects.join(', ')
				: '',
			maxPerRun: Number(config.max_updates_per_run) || DEFAULT_POLICY_STATE.maxPerRun,
			skipWhenRunning: config.skip_when_running !== false,
			window: config.window === 'idle' ? 'idle' : 'next_restart',
			idleMinutes: Number(config.idle_minutes) || DEFAULT_POLICY_STATE.idleMinutes
		};
	} catch {
		return { ...DEFAULT_POLICY_STATE, enabled: task.status === enabledStatus };
	}
}

export function isCurrentServer(capturedServerId: string, currentServerId: string): boolean {
	return capturedServerId === currentServerId;
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
		window: form.window ?? 'next_restart',
		idle_minutes: form.window === 'idle' ? (form.idleMinutes ?? 15) : 0
	});
}
