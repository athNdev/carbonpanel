// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Merged in the MINE-172 integration: this file existed independently on
// both lines of development, so the union of both test sets is kept.

package rcon

import (
	"context"
	"net"
	"testing"
	"time"
)

// The wrapper's only job is timeout handling around the blocking client,
// so pin both exit paths without needing a real server.
func TestSendCommand_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := SendCommand(ctx, "127.0.0.1", 25575, "pw", "list")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("cancelled call took %v", time.Since(start))
	}
}

func TestSendCommand_UnreachableHostFails(t *testing.T) {
	// Nothing listens here: connection refused must surface as an error,
	// not a hang (bounded by the dial timeout inside the client or ours).
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := SendCommand(ctx, "127.0.0.1", 1, "pw", "list"); err == nil {
		t.Error("expected error dialing closed port")
	}
}

func TestSendCommand_TimeoutWinsOverHang(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _ = SendCommand(ctx, "10.255.255.1", 25575, "pw", "list")
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("call outlived context by far: %v", elapsed)
	}
}

func TestSendCommand_TimeoutClosesTheConnection(t *testing.T) {
	// A server that accepts and never answers: the timeout must not leave
	// the exchange goroutine blocked forever. Collapsing the socket deadline
	// unblocks the read and the connection is closed, which the listener
	// observes as a peer disconnect.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr == nil {
			accepted <- conn
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _ = SendCommand(ctx, "127.0.0.1", addr.Port, "pw", "list")
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("call took %v, want ~150ms", elapsed)
	}

	// The peer must observe the disconnect shortly after the timeout.
	closed := make(chan struct{})
	go func() {
		var conn net.Conn
		select {
		case conn = <-accepted:
		case <-time.After(2 * time.Second):
			close(closed)
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 64)
		if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			close(closed)
			return
		}
		for {
			if _, readErr := conn.Read(buf); readErr != nil {
				break // EOF or reset: our side closed the connection
			}
		}
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("connection was not closed after the timeout; the exchange goroutine leaked")
	}
}

// --- tests present only on the pre-merge line of development ---

func TestSendCommandCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Unroutable host: dial blocks, so the already-cancelled context wins.
	_, err := SendCommand(ctx, "10.255.255.1", 25575, "pw", "list")
	if err != context.Canceled {
		t.Fatalf("err=%v want context.Canceled", err)
	}
}

func TestSendCommandRefusedHostErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Nothing listens on port 1: dial fails fast with a wrapped error.
	if _, err := SendCommand(ctx, "127.0.0.1", 1, "pw", "list"); err == nil {
		t.Fatal("want error for connection-refused host")
	}
}
