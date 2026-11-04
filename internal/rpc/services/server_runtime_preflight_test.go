// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/docker"
)

// The compatibility rules themselves are covered at the resolved-config seam
// in internal/docker/runtime_profile_test.go. This covers the caller contract:
// an incompatible image/runtime combination reaches the user as a
// failed-precondition that names the reason, and no other create failure
// leaks daemon internals.
func TestCreateContainerErrorReportsAnIncompatibleRuntime(t *testing.T) {
	cause := fmt.Errorf("%w: %s [fatal]: /data is mounted read-only; make it writable",
		docker.ErrRuntimeIncompatible, "data_mount_read_only")
	err := createContainerError(fmt.Errorf("failed to create container: %w", cause))

	if err.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v, want failed_precondition", err.Code())
	}
	if !strings.Contains(err.Message(), "data_mount_read_only") {
		t.Fatalf("message = %q, want the compatibility reason", err.Message())
	}
}

func TestCreateContainerErrorHidesOtherCreateFailures(t *testing.T) {
	err := createContainerError(errors.New("daemon said: /var/run/docker.sock permission denied"))

	if err.Code() != connect.CodeInternal {
		t.Fatalf("code = %v, want internal", err.Code())
	}
	if strings.Contains(err.Message(), "docker.sock") {
		t.Fatalf("message = %q, want the generic text", err.Message())
	}
}
