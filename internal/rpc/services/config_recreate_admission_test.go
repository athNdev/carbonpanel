// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"strings"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// TestConfigRecreateIsGated proves a config-driven container recreation
// acquires the shared node gate before any Docker call (OSS-405). A held gate
// makes recreation fail closed.
func TestConfigRecreateIsGated(t *testing.T) {
	svc := NewConfigService(nil, nil, nil, nil, logger.New())
	release, err := svc.admission.Acquire(context.Background(), "node-config")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = svc.recreateContainer(ctx, &storage.Server{NodeID: "node-config", ContainerID: "container-config"}, &storage.ServerConfig{})
	if err == nil || !strings.Contains(err.Error(), "node node-config start admission") {
		t.Fatalf("recreateContainer error = %v, want node admission refusal", err)
	}
}
