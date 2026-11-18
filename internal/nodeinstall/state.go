// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package nodeinstall implements the portable, mode-agnostic guest installer
// shared by every managed Carbon Panel runtime mode (host-native private
// daemon, QCOW2 VM, unprivileged LXC). It owns only the directories passed to
// it by the caller: it never changes global host configuration, never starts
// or stops services and never writes outside the state/data directories.
//
// The enrollment material it produces consumes the SHARED contract from
// ADR 0019 (proto/carbonpanel/v1/node_control.proto). It does not fork, copy
// or re-declare that contract: it imports the generated Go messages and fills
// them directly.
package nodeinstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Distinct, stable refusal codes. Callers (and fixtures) match these with
// errors.Is; none of them carries secret material.
var (
	ErrInvalidInput       = errors.New("nodeinstall: invalid input")
	ErrNotFound           = errors.New("nodeinstall: not found")
	ErrAlreadyExists      = errors.New("nodeinstall: already exists")
	ErrConsumed           = errors.New("nodeinstall: one-time material already consumed")
	ErrIntegrity          = errors.New("nodeinstall: integrity check failed")
	ErrPlatform           = errors.New("nodeinstall: platform mismatch")
	ErrUnresolvedRecovery = errors.New("nodeinstall: unresolved recovery blocks retry")
	ErrSecretLeak         = errors.New("nodeinstall: secret material detected in exported metadata")
)

const (
	identityFile       = "identity.json"
	nodeKeyFile        = "node.key"
	bundleFile         = "bundle.json"
	createRequestFile  = "create_enrollment_request.json"
	enrollmentSecret   = "enrollment.secret"
	enrollmentMetaFile = "enrollment.meta"
	claimRequestFile   = "claim_request.json"
	claimConsumed      = "claim.consumed"
	stateFile          = "state.json"
	volumeBindingFile  = "volume.json"
	slotA              = "a"
	slotB              = "b"
	manifestName       = "manifest.json"
)

// safeStateDir validates that stateDir is non-empty and confined. It refuses
// the filesystem root and empty paths so a caller cannot accidentally point
// the installer at global host configuration.
func safeStateDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("%w: state directory is required", ErrInvalidInput)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("%w: state directory: %v", ErrInvalidInput, err)
	}
	clean := filepath.Clean(abs)
	if clean == string(filepath.Separator) || clean == filepath.VolumeName(clean)+string(filepath.Separator) {
		return "", fmt.Errorf("%w: state directory must not be the filesystem root", ErrInvalidInput)
	}
	return clean, nil
}

// writeFile writes data with an explicit mode, replacing an existing file
// atomically. On Windows os.Rename cannot overwrite, so the destination is
// removed immediately before the rename; a crash can at worst leave no file,
// never a torn one.
func writeFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".nodeinstall-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return nil
}

func writeJSON(path string, v any, perm os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(b, '\n'), perm)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrIntegrity, path, err)
	}
	return nil
}
