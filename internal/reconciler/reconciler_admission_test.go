// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package reconciler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
)

// TestSelfHealRecreateIsGated proves a self-heal that starts a replacement
// container acquires the shared node gate before any Docker mutation (OSS-405).
func TestSelfHealRecreateIsGated(t *testing.T) {
	server := &db.Server{ID: "s1", Name: "Survival", NodeID: "node-heal", ContainerID: "c1", Status: db.StatusRunning}
	store := newFakeStore(server)
	cli := &fakeClient{}
	r := newTestReconciler(t, store, cli, &fakeProxy{}, &fakeBus{})

	release, err := r.admission.Acquire(context.Background(), "node-heal")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = r.selfHealRecreate(ctx, cli, server)
	if err == nil || !strings.Contains(err.Error(), "node node-heal start admission") {
		t.Fatalf("selfHealRecreate error = %v, want node admission refusal", err)
	}
	if len(store.containers) != 0 {
		t.Fatalf("refused self-heal recorded container mutations: %+v", store.containers)
	}
}
