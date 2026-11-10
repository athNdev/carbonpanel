// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

export const MEMORY_LIMIT_UNSUPPORTED = 'memory_limit_unsupported';
export const ROOTLESS_CGROUP_UNQUALIFIED = 'rootless_cgroup_unqualified';
export const WRITABLE_LAYER_CAP_UNSUPPORTED = 'writable_layer_cap_unsupported';
export const STORAGE_QUOTA_IS_NOT_A_WORLD_QUOTA = 'storage_quota_is_not_a_world_quota';

const MEMORY_ADVISORY_WARNINGS = new Set([MEMORY_LIMIT_UNSUPPORTED, ROOTLESS_CGROUP_UNQUALIFIED]);

const WORLD_DISK_ADVISORY_WARNINGS = new Set([
	WRITABLE_LAYER_CAP_UNSUPPORTED,
	STORAGE_QUOTA_IS_NOT_A_WORLD_QUOTA
]);

export type LimitKind = 'enforced' | 'advisory' | 'configured' | 'admission';

export interface ResourceEnforcementLike {
	requestedMemoryBytes?: bigint | number;
	effectiveMemoryBytes?: bigint | number;
	worldDiskEnforced?: boolean;
	worldQuotaAdmission?: boolean;
	memoryEnforced?: boolean;
	warnings?: readonly string[];
}

export interface LimitCopy {
	kind: LimitKind;
	label: string;
}

function warningsOf(enforcement: ResourceEnforcementLike | null | undefined): string[] {
	return [...(enforcement?.warnings ?? [])];
}

/**
 * Configured RAM is enforced only when the API says so and RuntimeProfile did
 * not warn that the daemon cannot apply the cap.
 */
export function memoryLimitCopy(
	enforcement: ResourceEnforcementLike | null | undefined
): LimitCopy {
	const warnings = warningsOf(enforcement);
	if (warnings.some((code) => MEMORY_ADVISORY_WARNINGS.has(code))) {
		return { kind: 'advisory', label: 'Advisory (not enforced by this engine)' };
	}
	if (enforcement?.memoryEnforced) {
		return { kind: 'enforced', label: 'Enforced' };
	}
	return { kind: 'configured', label: 'Configured' };
}

/**
 * storage-opt size never becomes a world quota. Panel admission is named
 * separately from kernel enforcement.
 */
export function worldDiskCopy(enforcement: ResourceEnforcementLike | null | undefined): LimitCopy {
	const warnings = warningsOf(enforcement);
	if (warnings.some((code) => WORLD_DISK_ADVISORY_WARNINGS.has(code))) {
		return { kind: 'advisory', label: 'Writable-layer cap is not a world quota' };
	}
	if (enforcement?.worldDiskEnforced) {
		return { kind: 'enforced', label: 'World disk enforced' };
	}
	if (enforcement?.worldQuotaAdmission) {
		return { kind: 'admission', label: 'World quota is panel admission, not a kernel quota' };
	}
	return { kind: 'advisory', label: 'World disk is not quota-limited' };
}

export function ignoresResourceWarnings(
	enforcement: ResourceEnforcementLike | null | undefined
): boolean {
	const warnings = warningsOf(enforcement);
	if (warnings.some((code) => MEMORY_ADVISORY_WARNINGS.has(code))) {
		return memoryLimitCopy(enforcement).kind === 'enforced';
	}
	if (warnings.some((code) => WORLD_DISK_ADVISORY_WARNINGS.has(code))) {
		return worldDiskCopy(enforcement).kind === 'enforced';
	}
	return false;
}

export type ExitFailureKind = 'oom' | 'forced-stop' | 'other';

/** OOM is only the Docker OOMKilled reason. Exit 137 after a stop is not OOM. */
export function exitFailureKind(reason: string | null | undefined): ExitFailureKind {
	const text = (reason ?? '').trim();
	if (/oom-?killed/i.test(text)) {
		return 'oom';
	}
	if (/exited with code 137/i.test(text)) {
		return 'forced-stop';
	}
	return 'other';
}

export function exitFailureCopy(reason: string | null | undefined): string {
	const text = (reason ?? '').trim();
	if (!text) {
		return 'Daemon status: ERROR';
	}
	switch (exitFailureKind(text)) {
		case 'oom':
			return `Out of memory: ${text}`;
		case 'forced-stop':
			return `Forced stop: ${text}`;
		default:
			return `Server failed: ${text}`;
	}
}
