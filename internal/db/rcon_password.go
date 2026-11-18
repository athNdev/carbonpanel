// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

const rconPasswordBytes = 24

// GenerateRCONPassword returns a high-entropy password for a new server. A
// CSPRNG failure is returned as an error, never as a predictable string: a
// sentinel stored as a credential would be a known password. Callers must fail
// closed (blank rcon.password disables RCON in Minecraft).
func GenerateRCONPassword() (string, error) {
	buf := make([]byte, rconPasswordBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate RCON password: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// IsWeakRCONPassword reports a persisted value that cannot be treated as a
// secret. Empty values are reported separately because Minecraft treats a
// blank rcon.password as "RCON disabled" (the migration must not turn a
// deployment that intentionally had no RCON into a startup failure).
func IsWeakRCONPassword(password string) bool {
	if password == "" {
		return true
	}
	return strings.HasPrefix(password, "CARBONPANEL_")
}
