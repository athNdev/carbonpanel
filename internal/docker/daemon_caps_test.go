// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"testing"

	"github.com/moby/moby/api/types/system"
)

func TestDaemonRootlessFromInfo(t *testing.T) {
	tests := map[string]struct {
		info system.Info
		want bool
	}{
		"rootful default": {info: system.Info{}, want: false},
		"security opt name=rootless": {
			info: system.Info{SecurityOptions: []string{"name=rootless"}},
			want: true,
		},
		"warning substring": {
			info: system.Info{Warnings: []string{"WARNING: Running in rootless-mode"}},
			want: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := DaemonRootlessFromInfo(tc.info); got != tc.want {
				t.Fatalf("DaemonRootlessFromInfo() = %t, want %t", got, tc.want)
			}
		})
	}
}
