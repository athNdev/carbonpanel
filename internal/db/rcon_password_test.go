// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGenerateRCONPasswordIsRandomAndStrong(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 8; i++ {
		password, err := GenerateRCONPassword()
		require.NoError(t, err)
		raw, err := hex.DecodeString(password)
		require.NoError(t, err)
		require.Len(t, raw, rconPasswordBytes)
		require.False(t, IsWeakRCONPassword(password))
		require.False(t, seen[password], "generated passwords must not repeat")
		seen[password] = true
	}
}

func TestIsWeakRCONPassword(t *testing.T) {
	require.True(t, IsWeakRCONPassword(""))
	require.True(t, IsWeakRCONPassword("CARBONPANEL_default"))
	require.True(t, IsWeakRCONPassword("CARBONPANEL_1234abcd"))
	require.False(t, IsWeakRCONPassword("b7f3a1c9d2e4f6a8b0c2d4e6"))
}

func TestCreateDefaultServerConfigUsesStrongPassword(t *testing.T) {
	store, err := NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{
		Path:           filepath.Join(t.TempDir(), "rcon.db"),
		MaxConnections: 5,
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.Migrate())

	cfg := store.CreateDefaultServerConfig("srv-abcdefgh")
	require.NotNil(t, cfg.RCONPassword)
	require.False(t, IsWeakRCONPassword(*cfg.RCONPassword))
}
