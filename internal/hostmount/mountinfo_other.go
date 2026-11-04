// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !linux

package hostmount

// hostMounts is unavailable on this platform. The daemon-backed check in
// Resolve still applies, so a container on a non-Linux host is still verified.
func hostMounts() ([]mountEntry, bool) { return nil, false }
