// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"errors"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestApplyModuleConfinementNeverAutoBroadensPrivilege(t *testing.T) {
	hc := &container.HostConfig{Privileged: true}
	applyModuleConfinement(hc)
	if hc.Privileged {
		t.Fatal("module create must not leave Privileged true; host-extension privilege is an operator feature, not a default")
	}
	applyModuleConfinement(nil)
}

func TestHostExtensionDockerSocketIsRefused(t *testing.T) {
	err := ValidateModuleVolumeMounts([]ModuleVolumeMount{{
		Source: "/var/run/docker.sock",
		Target: "/var/run/docker.sock",
		Type:   "bind",
	}}, "/data/server")
	if err == nil {
		t.Fatal("a docker.sock bind is a host-extension and must not be auto-mounted")
	}
	if !errors.Is(err, ErrDockerVolumeOutsideSafeRoot) {
		t.Fatalf("err = %v, want ErrDockerVolumeOutsideSafeRoot so privilege is not auto-broadened", err)
	}
}
