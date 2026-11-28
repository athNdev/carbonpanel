// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

/**
 * Baseline state for the server configuration form. The component keeps these
 * as reactive maps/sets; this module works on their plain `Map`/`Set` shape so
 * the in-flight edit reconciliation can be tested without a DOM.
 */
export interface ConfigFormState {
	originalValues: ReadonlyMap<string, string | null>;
	originalEnabled: ReadonlySet<string>;
	currentValues: ReadonlyMap<string, string | null>;
	currentEnabled: ReadonlySet<string>;
}

export interface RebaselinedConfigForm {
	originalValues: Map<string, string | null>;
	originalEnabled: Set<string>;
	currentValues: Map<string, string | null>;
	currentEnabled: Set<string>;
}

/**
 * Move the baseline to exactly what a successful save sent, while restoring
 * entries the user edited while the request was in flight as dirty edits
 * against that baseline.
 *
 * `inFlightValues`/`inFlightEnabled` must be captured after the RPC resolves
 * and before the response refresh overwrites the form: they hold the latest
 * user edits. Capturing before the request would silently drop anything typed
 * during it, including edits to fields that were clean when Save started.
 */
export function rebaselineConfigAfterSave(
	state: ConfigFormState,
	sent: Record<string, string>,
	inFlightValues: ReadonlyMap<string, string | null>,
	inFlightEnabled: ReadonlySet<string>
): RebaselinedConfigForm {
	const baselineValues = new Map(state.originalValues);
	const baselineEnabled = new Set(state.originalEnabled);
	const editedValues = new Map(state.currentValues);
	const editedEnabled = new Set(state.currentEnabled);

	for (const [key, value] of Object.entries(sent)) {
		const sentValue = value === '' ? null : value;
		const sentOn = value !== '';
		baselineValues.set(key, sentValue);
		if (sentOn) baselineEnabled.add(key);
		else baselineEnabled.delete(key);

		const editedValue = inFlightValues.get(key) ?? null;
		const editedOn = inFlightEnabled.has(key);
		if (editedValue === sentValue && editedOn === sentOn) {
			// The entry still matches what was sent: it is clean now.
			editedValues.set(key, sentValue);
			if (sentOn) editedEnabled.add(key);
			else editedEnabled.delete(key);
		} else {
			// Edited while the save ran: keep it as a dirty edit.
			editedValues.set(key, editedValue);
			if (editedOn) editedEnabled.add(key);
			else editedEnabled.delete(key);
		}
	}

	// Entries outside the sent batch: an edit typed mid-flight for a key
	// that was not dirty when the save started must also survive.
	for (const [key, value] of inFlightValues) {
		if (key in sent) continue;
		if (
			value !== (baselineValues.get(key) ?? null) ||
			inFlightEnabled.has(key) !== baselineEnabled.has(key)
		) {
			editedValues.set(key, value ?? null);
			if (inFlightEnabled.has(key)) editedEnabled.add(key);
			else editedEnabled.delete(key);
		}
	}

	return {
		originalValues: baselineValues,
		originalEnabled: baselineEnabled,
		currentValues: editedValues,
		currentEnabled: editedEnabled
	};
}
