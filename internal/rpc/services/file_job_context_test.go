// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/pkg/logger"
)

// TestFileServiceOwnsDetachedJobContext verifies that background extraction
// and download work is owned by the service, not by the RPC that started it:
// a cancelled request must not cancel the job context, while Stop must cancel
// it and wait for the job's cleanup.
func TestFileServiceOwnsDetachedJobContext(t *testing.T) {
	svc := NewFileService(nil, nil, nil, nil, logger.New())
	detachedCtx, release := svc.beginDetached()

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	if requestCtx.Err() == nil {
		t.Fatal("request fixture must be cancelled")
	}
	if detachedCtx.Err() != nil {
		t.Fatalf("request cancellation leaked into the owned job context: %v", detachedCtx.Err())
	}

	stopped := make(chan struct{})
	go func() {
		svc.Stop()
		close(stopped)
	}()

	select {
	case <-detachedCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not cancel the owned job context")
	}

	select {
	case <-stopped:
		t.Fatal("Stop returned before the detached job released its slot")
	case <-time.After(50 * time.Millisecond):
	}
	release()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return after the job released")
	}
}
