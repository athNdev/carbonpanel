// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"encoding/json"
)

// ownedSnapshot is the serialised form of the in-memory enrollment store used
// by the restart fixture. It contains no plaintext secret: the node-generated
// secret S is never stored, only its salted digest and approved-key metadata.
type ownedSnapshot struct {
	Enrollments    map[string]*Enrollment `json:"enrollments"`
	RevokedSerials map[string]bool        `json:"revoked_serials"`
}

func jsonSnapshot(enrollments map[string]*Enrollment, revoked map[string]bool) ([]byte, error) {
	return json.Marshal(ownedSnapshot{Enrollments: enrollments, RevokedSerials: revoked})
}

func jsonRestore(data []byte, enrollments *map[string]*Enrollment, revoked *map[string]bool) error {
	var s ownedSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s.Enrollments == nil {
		s.Enrollments = make(map[string]*Enrollment)
	}
	if s.RevokedSerials == nil {
		s.RevokedSerials = make(map[string]bool)
	}
	*enrollments = s.Enrollments
	*revoked = s.RevokedSerials
	return nil
}
