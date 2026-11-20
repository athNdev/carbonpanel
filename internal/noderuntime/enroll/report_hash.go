// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package enroll

import (
	"crypto/sha256"

	"google.golang.org/protobuf/proto"
)

// ReportHash binds a preflight or health report payload to its node-operation
// envelope using SHA-256 over deterministic protobuf bytes.
func ReportHash(report proto.Message) ([]byte, error) {
	payload, err := (proto.MarshalOptions{Deterministic: true}).Marshal(report)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(payload)
	return digest[:], nil
}
