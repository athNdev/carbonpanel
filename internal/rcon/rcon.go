// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rcon

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	gorcon "github.com/jltobler/go-rcon"
)

type rconResult struct {
	output string
	err    error
}

// SendCommand sends one RCON command and returns the server's response.
//
// The whole exchange — TCP dial, RCON authentication, and the command
// round-trip — is bounded by ctx. The go-rcon client has no context support
// (its Client.Send dials and reads with no deadline), so a server that
// accepts the connection but never answers used to leave the exchange
// goroutine blocked forever. Here the socket carries ctx's deadline, and a
// cancellation collapses the deadline to "now": the blocked read fails, the
// goroutine closes the connection, and nothing is left behind this call.
func SendCommand(ctx context.Context, RCONHost string, RCONPort int, RCONPassword string, command string) (string, error) {
	// An already-cancelled context wins before any I/O is attempted, so the
	// error is exactly the context error callers may compare against.
	if err := ctx.Err(); err != nil {
		return "", err
	}

	dialer := &net.Dialer{}
	raw, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(RCONHost, strconv.Itoa(RCONPort)))
	if err != nil {
		return "", fmt.Errorf("failed to establish connection: %w", err)
	}

	// Bound the RCON exchange (authentication + command) by ctx's deadline.
	if deadline, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(deadline)
	}

	// Collapse the deadline when ctx is cancelled without one (or early):
	// the in-flight read fails immediately and the exchange goroutine exits
	// after closing the socket.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = raw.SetDeadline(time.Now())
		case <-stop:
		}
	}()

	resultCh := make(chan rconResult, 1)
	go func() {
		conn, authErr := gorcon.NewConn(raw, RCONPassword)
		if authErr != nil {
			// NewConn closes the socket on authentication failure; the extra
			// close is harmless and covers any error path that does not.
			_ = raw.Close()
			resultCh <- rconResult{err: authErr}
			return
		}
		defer func() { _ = conn.Close() }()
		output, sendErr := conn.SendCommand(command)
		resultCh <- rconResult{output: output, err: sendErr}
	}()

	// wait for either the command result or a timeout
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-resultCh:
		if result.err != nil {
			return "", result.err
		}
		return result.output, nil
	}
}
