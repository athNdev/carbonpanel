// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build linux

package hostmount

import "os"

// hostMounts reads this process's mount table. It returns false when the table
// cannot be read, which leaves the decision to the daemon-backed check.
func hostMounts() ([]mountEntry, bool) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, false
	}
	defer file.Close()
	return parseMountinfo(file), true
}
