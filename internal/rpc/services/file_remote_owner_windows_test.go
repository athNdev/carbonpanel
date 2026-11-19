// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build windows

package services

import "testing"

// Windows has no uid/gid model for bind-mounted server data; the cross-platform
// fault-injection test still covers destination bytes and mode preservation.
func TestRemoteSavePreservesInstalledOwnerAndMode(t *testing.T) {
	t.Skip("uid/gid ownership is not meaningful on Windows")
}
