// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scheduler

import (
	"testing"

	"github.com/athNdev/carbon-panel/pkg/files"
)

func TestDefaultBackupExcludesWorkTemp(t *testing.T) {
	if !files.PathExcluded(".tmp/modrinth.mrpack", defaultBackupExcludes) {
		t.Fatal("work temp downloads must stay out of world backups")
	}
	if !files.PathExcluded(".tmp/libnetty.so", defaultBackupExcludes) {
		t.Fatal("native libraries in the work temp must stay out of world backups")
	}
	if files.PathExcluded("world/level.dat", defaultBackupExcludes) || files.PathExcluded("mods/example.jar", defaultBackupExcludes) {
		t.Fatal("world and mod files must stay in backups")
	}
}
