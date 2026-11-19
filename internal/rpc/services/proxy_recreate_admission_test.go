// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// TestProxyRecreateIsGated proves a routing change that recreates a running
// container acquires the shared node gate before touching Docker (OSS-405).
func TestProxyRecreateIsGated(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()
	svc := NewProxyService(store, nil, nil, nil, nil, nil, logger.New())

	server := &storage.Server{
		ID: "srv", Name: "srv", NodeID: "node-proxy", ContainerID: "container-proxy",
		ProxyHostname: "old.example", Status: storage.StatusRunning,
		Port: 25565, Memory: 1024, DataPath: t.TempDir(),
	}
	if err := store.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}

	release, err := svc.admission.Acquire(context.Background(), "node-proxy")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = svc.UpdateServerRouting(ctx, connect.NewRequest(&v1.UpdateServerRoutingRequest{ServerId: "srv"}))
	if err == nil || connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("UpdateServerRouting error = %v, want deadline-exceeded admission refusal", err)
	}
	got, getErr := store.GetServer(context.Background(), "srv")
	if getErr != nil || got.ContainerID != "container-proxy" {
		t.Fatalf("refused routing change changed server = %+v, %v", got, getErr)
	}
}
