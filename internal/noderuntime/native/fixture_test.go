// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import "time"

// fixtureTime is the deterministic clock shared by the host-native fixtures.
// Enrollment fixtures live in the shared enroll package; this one only serves
// plan/preflight fixtures that remain mode-specific.
func fixtureTime() time.Time {
	t, _ := time.Parse(time.RFC3339, "2026-11-18T12:00:00Z")
	return t
}
