// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

func TestToProtoServerDoesNotClaimEnforcedLimitsWithoutDaemon(t *testing.T) {
	svc := NewServerService(nil, nil, nil, &config.Config{
		Storage: config.StorageConfig{ContainerWritableMB: 256, WorldQuotaMB: 1024},
	}, nil, nil, nil, nil, nil, logger.New(), nil, nil, nil)
	got := svc.toProtoServer(context.Background(), &storage.Server{ID: "s1", Memory: 2048, WorldSize: 10 << 20})
	if got.ResourceEnforcement == nil {
		t.Fatal("ResourceEnforcement is nil")
	}
	en := got.ResourceEnforcement
	if en.MemoryEnforced {
		t.Fatal("MemoryEnforced = true, want false without /info")
	}
	if en.WorldDiskEnforced {
		t.Fatal("WorldDiskEnforced = true, want false: storage-opt is not a world quota")
	}
	if !en.WorldQuotaAdmission {
		t.Fatal("WorldQuotaAdmission = false, want true when world_quota_mb is set")
	}
	if en.RequestedMemoryBytes != 2048<<20 {
		t.Fatalf("RequestedMemoryBytes = %d, want 2048 MiB", en.RequestedMemoryBytes)
	}
	if !slices.Contains(en.Warnings, string(docker.RuntimeIssueStorageQuotaIsNotAWorldQuota)) {
		t.Fatalf("warnings = %v, want storage_quota_is_not_a_world_quota", en.Warnings)
	}
}

func TestStatusReasonKeepsOOMDistinctFromForcedStop(t *testing.T) {
	oom := dbServerToProto(&storage.Server{Status: storage.StatusError, StatusReason: "container was OOM-killed (exit code 137)"})
	stop := dbServerToProto(&storage.Server{Status: storage.StatusError, StatusReason: "container exited with code 137"})
	if !strings.Contains(oom.StatusReason, "OOM") {
		t.Fatalf("OOM reason = %q, want OOM", oom.StatusReason)
	}
	if strings.Contains(stop.StatusReason, "OOM") {
		t.Fatalf("forced-stop reason = %q, must not be classified as OOM", stop.StatusReason)
	}
	if oom.StatusReason == stop.StatusReason {
		t.Fatal("OOM and forced-stop reasons must stay distinct")
	}
}
