// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build windows

package files

// ownerOf has no meaning on Windows: there is no uid/gid to adopt, and the
// Windows-native development path uses Docker Desktop's file sharing instead of
// a bind mount owned by another user. Reporting "unknown" keeps every caller a
// no-op instead of failing.
func ownerOf(string) (ownership, bool) { return ownership{}, false }
