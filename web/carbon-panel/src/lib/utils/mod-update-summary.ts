// SPDX-License-Identifier: MPL-2.0
//
// Summary line for an installed-mod update preview. Every preview state the
// backend can return is counted, so the line never implies that only
// `available` rows were checked: a file that needs review or could not be
// identified is a real outcome of the check.

const LABELS: ReadonlyArray<readonly [string, string, string]> = [
	['available', 'update available', 'updates available'],
	['current', 'current', 'current'],
	['review_required', 'review required', 'review required'],
	['unknown_source', 'unknown source', 'unknown source'],
	['unavailable', 'unavailable', 'unavailable']
];

/** One entry per installed file; the state is the preview state, if any. */
export function summarizeModUpdateCheck(states: readonly (string | undefined)[]): string {
	const total = states.length;
	const counts = new Map<string, number>();
	for (const state of states) {
		if (!state) continue;
		counts.set(state, (counts.get(state) ?? 0) + 1);
	}

	const parts: string[] = [];
	for (const [state, one, many] of LABELS) {
		const count = counts.get(state);
		if (!count) continue;
		parts.push(`${count} ${count === 1 ? one : many}`);
	}

	const detail = parts.length ? `: ${parts.join(', ')}` : '';
	return `Checked ${total} installed file${total === 1 ? '' : 's'}${detail}.`;
}
