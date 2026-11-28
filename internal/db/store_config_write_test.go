// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/stretchr/testify/require"
)

// TestSaveServerConfigTakesWriterLockWithoutConfigRow proves the writer lock
// does not depend on the row existing: a legacy server can reach the guard and
// the save with no persisted config yet.
func TestSaveServerConfigTakesWriterLockWithoutConfigRow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "panel.db")
	open := func() *Store {
		store, err := NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{
			Path: path, AutoMigrate: true, MaxConnections: 5,
		}})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	store := open()
	competing := open()

	server := &Server{
		ID: "config-writer-lock-absent", Name: "config-writer-lock-absent",
		ModLoader: ModLoaderFabric, MCVersion: "1.21.1",
	}
	require.NoError(t, store.CreateServer(ctx, server))
	_, err := store.AddStagedContent(ctx, reviewedModCandidateRow(server.ID))
	require.NoError(t, err)
	saved, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NoError(t, store.DB().Where("server_id = ?", server.ID).Delete(&ServerConfig{}).Error)
	stop := make(chan struct{})
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = competing.DB().Exec("UPDATE servers SET name = name WHERE id = ?", server.ID).Error
			}
		}
	}()

	var failures atomic.Int64
	for i := 0; i < 600; i++ {
		motd := fmt.Sprintf("absent-%d", i)
		saved.MOTD = &motd
		if saveErr := store.SaveServerConfig(ctx, saved); saveErr != nil {
			failures.Add(1)
		}
	}
	close(stop)
	writers.Wait()
	require.Zero(t, failures.Load(), "config saves without a config row must still take the writer lock first")
}

// TestSaveServerConfigTakesWriterLockBeforeGuardReads is the regression for
// SQLITE_BUSY_SNAPSHOT on concurrent config saves. The reviewed-mod guard used
// to read first inside a deferred transaction; a competing commit between that
// read and the save turned the write upgrade into "database is locked" (busy
// snapshot), which busy_timeout does not retry. The save must take SQLite's
// writer lock before any guard read, so the read and the save share one writer
// snapshot.
func TestSaveServerConfigTakesWriterLockBeforeGuardReads(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "panel.db")
	open := func() *Store {
		store, err := NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{
			Path: path, AutoMigrate: true, MaxConnections: 5,
		}})
		require.NoError(t, err)
		t.Cleanup(func() { _ = store.Close() })
		return store
	}
	// Two handles on one database file: the subject save and the competing
	// writer each own a connection pool, as two panel processes or a WAL reader
	// would.
	store := open()
	competing := open()

	server := &Server{
		ID: "config-writer-lock", Name: "config-writer-lock",
		ModLoader: ModLoaderFabric, MCVersion: "1.21.1",
	}
	require.NoError(t, store.CreateServer(ctx, server))
	_, err := store.AddStagedContent(ctx, reviewedModCandidateRow(server.ID))
	require.NoError(t, err, "the fixture needs a staged reviewed candidate so the guard reads run")

	stop := make(chan struct{})
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				// Any commit advances the WAL snapshot the save must not
				// straddle.
				_ = competing.DB().Exec("UPDATE servers SET name = name WHERE id = ?", server.ID).Error
			}
		}
	}()

	live, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	const iterations = 3000
	var failures atomic.Int64
	var mu sync.Mutex
	byMessage := map[string]int{}
	for i := 0; i < iterations; i++ {
		next := *live
		motd := fmt.Sprintf("motd-%d", i)
		next.MOTD = &motd
		if saveErr := store.SaveServerConfig(ctx, &next); saveErr != nil {
			failures.Add(1)
			mu.Lock()
			byMessage[saveErr.Error()]++
			mu.Unlock()
			continue
		}
		*live = next
	}
	close(stop)
	writers.Wait()

	require.Zero(t, failures.Load(),
		"%d/%d config saves failed against a competing writer: %v",
		failures.Load(), iterations, byMessage)

	// The guard itself stays intact: a runtime-target move is refused and
	// leaves the live row unchanged, while non-runtime saves above succeeded.
	moved, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("motd-%d", iterations-1), *moved.MOTD,
		"a non-runtime save under a staged reviewed mod must persist")
	version := "1.21.4"
	moved.Version = &version
	require.ErrorIs(t, store.SaveServerConfig(ctx, moved), ErrStagedModVersionChange)

	after, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, "1.21.1", *after.Version, "a refused runtime save must not commit")
	require.True(t, strings.EqualFold("fabric", *after.Type))
}
