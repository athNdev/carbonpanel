// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !unix

package docker

// dataOwnerUID is unavailable on this platform, so the owner-mismatch check is
// skipped rather than guessed.
func dataOwnerUID(string) (int, bool) { return 0, false }
