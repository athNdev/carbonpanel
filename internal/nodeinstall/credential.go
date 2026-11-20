// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, you can obtain one at https://mozilla.org/MPL/2.0/.

package nodeinstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const managedCredentialFile = "managed-credential.json"

// ManagedCredential records the public certificate and the controller and
// daemon identities bound to the explicitly enrolled node.
type ManagedCredential struct {
	ControllerURL string                      `json:"controller_url"`
	DaemonID      string                      `json:"daemon_id"`
	Claim         *v1.ClaimEnrollmentResponse `json:"claim"`
}

func LoadManagedCredential(stateDir string) (*ManagedCredential, error) {
	dir, err := safeStateDir(stateDir)
	if err != nil {
		return nil, err
	}
	var credential ManagedCredential
	if err := readJSON(filepath.Join(dir, managedCredentialFile), &credential); err != nil {
		return nil, err
	}
	return &credential, nil
}

// SaveManagedCredential persists the public response from the only reserved
// live claim. An existing credential must be handled by explicit reenrollment.
func SaveManagedCredential(stateDir string, credential *ManagedCredential) error {
	if credential == nil || credential.ControllerURL == "" || credential.DaemonID == "" ||
		len(credential.Claim.GetEnrolledClientCertificateDer()) == 0 {
		return fmt.Errorf("%w: incomplete managed credential", ErrInvalidInput)
	}
	dir, err := safeStateDir(stateDir)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, managedCredentialFile)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%w: managed credential already exists", ErrAlreadyExists)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeJSON(path, credential, 0o600)
}
