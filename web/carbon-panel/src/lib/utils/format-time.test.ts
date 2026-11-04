// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

import test from 'node:test';
import assert from 'node:assert/strict';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';

import { formatTimestamp } from './format-time';

test('a real timestamp renders as a date, not "Invalid Date"', () => {
	// The bug this replaces: protobuf-es v2 timestamps have no toDate(), so the
	// old helper fell through to `new Date(object)` and printed "Invalid Date"
	// on every audit-log and backup row.
	const ts = timestampFromDate(new Date('2026-11-04T10:30:00Z'));
	const rendered = formatTimestamp(ts);

	assert.notEqual(rendered, 'Invalid Date');
	assert.ok(!rendered.includes('Invalid'), `rendered ${rendered}`);
	assert.match(rendered, /2026/);
});

test('a missing timestamp renders the fallback, not the epoch', () => {
	assert.equal(formatTimestamp(undefined), '—');
	assert.equal(formatTimestamp(null), '—');
	assert.equal(formatTimestamp(undefined, ''), '');
});
