// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt';

/**
 * Renders a protobuf Timestamp for a person to read.
 *
 * protobuf-es v2 timestamps are plain `{ seconds, nanos }` messages with no
 * methods. The previous `ts.toDate?.() ?? new Date(ts)` helper silently took the
 * second branch and printed "Invalid Date" on every table that used it.
 */
export function formatTimestamp(ts: Timestamp | undefined | null, fallback = '—'): string {
	if (!ts) return fallback;
	const date = timestampDate(ts);
	if (Number.isNaN(date.getTime())) return fallback;
	return date.toLocaleString();
}
