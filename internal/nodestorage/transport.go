// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodestorage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/athNdev/carbon-panel/internal/storageworker"
)

// Transport opens a confined accessor for a server data root on a remote node
// without shared controller storage (OSS-281).
type Transport interface {
	Open(ctx context.Context, node NodeInfo, dataRoot string) (Accessor, error)
}

var (
	transportMu sync.RWMutex
	transport   Transport
)

// SetTransport installs the process-wide remote storage transport. A nil
// transport leaves remote nodes without shared storage refused.
func SetTransport(t Transport) {
	transportMu.Lock()
	transport = t
	transportMu.Unlock()
}

func getTransport() Transport {
	transportMu.RLock()
	defer transportMu.RUnlock()
	return transport
}

// RequiresRemoteTransport reports whether the node owns storage only on the
// remote host and the controller must reach it through the worker channel.
// Shared-storage nodes (controller mount present or node root visible locally)
// keep the existing mapped local accessor.
func RequiresRemoteTransport(node NodeInfo) bool {
	if node.IsLocal {
		return false
	}
	if node.StorageRoot == "" {
		return false
	}
	if node.ControllerStorageRoot != "" {
		return false
	}
	mount := sharedStorageRoot(node)
	if fi, err := os.Stat(mount); err == nil && fi.IsDir() {
		return false
	}
	return true
}

// nodeDataRoot returns the node-side directory for one server's data tree.
func nodeDataRoot(node NodeInfo, dataRoot string) string {
	return filepath.Join(node.StorageRoot, filepath.Base(dataRoot))
}

// ProcessTransport runs the carbon-storage accessor worker as a subprocess.
type ProcessTransport struct {
	Bin string
}

// Open implements Transport.
func (p *ProcessTransport) Open(ctx context.Context, node NodeInfo, dataRoot string) (Accessor, error) {
	if p == nil || p.Bin == "" {
		return nil, fmt.Errorf("%w: worker binary is not configured", ErrUnavailable)
	}
	root := nodeDataRoot(node, dataRoot)
	sess, err := startProcessWorker(ctx, p.Bin, root)
	if err != nil {
		return nil, fmt.Errorf("%w: start storage worker for node %q: %v", ErrUnavailable, node.Name, err)
	}
	return storageworker.NewRemoteAccessor(sess), nil
}

func startProcessWorker(ctx context.Context, bin, dataRoot string) (*storageworker.Session, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cr, cw, err := os.Pipe()
	if err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "--mode=accessor", "--root", dataRoot)
	cmd.Stdin = pr
	cmd.Stdout = cw
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		_ = cr.Close()
		_ = cw.Close()
		return nil, err
	}
	// These are the child's descriptors. os/exec has inherited them, so only
	// the parent's copies close here; unlike io.Pipe, this does not close the
	// child's stdout stream before it can send the handshake.
	_ = pr.Close()
	_ = cw.Close()
	var closeOnce sync.Once
	var waitErr error
	ready := false
	closeF := func() error {
		closeOnce.Do(func() {
			// A malformed/refused handshake is not a usable session. Terminate
			// that owned child rather than waiting for it to honour stdin EOF.
			if !ready {
				_ = cmd.Process.Kill()
			}
			_ = pw.Close()
			waitErr = cmd.Wait()
			_ = cr.Close()
		})
		return waitErr
	}
	sess, err := storageworker.NewSession(pw, cr, closeF)
	if err != nil {
		_ = closeF()
		return nil, err
	}
	ready = true
	return sess, nil
}
