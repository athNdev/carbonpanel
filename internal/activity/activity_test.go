// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	storage "github.com/athNdev/carbon-panel/internal/db"
)

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testDBWithoutMigrate(t)
	if err := db.AutoMigrate(&storage.ActivityLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func testDBWithoutMigrate(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "_")
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DROP TABLE IF EXISTS activity_logs")
	})
	return db
}

func TestLogPersistsRequestIDAndDoesNotLeakAcrossServers(t *testing.T) {
	db := testDB(t)
	if err := Log(db, Entry{
		ActorID: "u1", Event: EventServerStart, SubjectTyp: "server", SubjectID: "s1",
		RequestID: "req-s1",
	}); err != nil {
		t.Fatalf("log s1: %v", err)
	}
	if err := Log(db, Entry{
		ActorID: "u2", Event: EventServerStop, SubjectTyp: "server", SubjectID: "s2",
		RequestID: "req-s2",
	}); err != nil {
		t.Fatalf("log s2: %v", err)
	}

	got, err := List(db, "s1", "", time.Time{}, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].RequestID != "req-s1" {
		t.Fatalf("expected s1's request id, got %+v", got)
	}
	for _, e := range got {
		if e.SubjectID != "s1" || e.RequestID == "req-s2" {
			t.Fatalf("cross-server activity leaked: %+v", e)
		}
	}
}

func TestRequestIDColumnIsAddedOnUpgrade(t *testing.T) {
	db := testDBWithoutMigrate(t)
	if err := db.Exec(`CREATE TABLE activity_logs (
		id text primary key,
		actor_id text,
		actor_name text,
		ip text,
		event text not null,
		subject_type text,
		subject_id text,
		properties text,
		created_at datetime
	)`).Error; err != nil {
		t.Fatalf("seed previous schema: %v", err)
	}
	if err := db.AutoMigrate(&storage.ActivityLog{}); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if err := Log(db, Entry{
		Event: EventServerStart, SubjectTyp: "server", SubjectID: "s1", RequestID: "req-upgrade",
	}); err != nil {
		t.Fatalf("log after upgrade: %v", err)
	}
	got, err := List(db, "s1", "", time.Time{}, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].RequestID != "req-upgrade" {
		t.Fatalf("expected request id after upgrade, got %+v", got)
	}
}

func TestLogAndList(t *testing.T) {
	db := testDB(t)
	if err := Log(db, Entry{ActorID: "u1", ActorName: "op", IP: "1.2.3.4", Event: EventServerStart, SubjectTyp: "server", SubjectID: "s1"}); err != nil {
		t.Fatalf("log: %v", err)
	}
	if err := Log(db, Entry{ActorID: "u2", Event: EventAuthLogin, SubjectTyp: "user", SubjectID: "u2"}); err != nil {
		t.Fatalf("log: %v", err)
	}
	got, err := List(db, "s1", "", time.Time{}, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Event != EventServerStart {
		t.Fatalf("expected 1 server.start record, got %+v", got)
	}
	got, err = List(db, "", "u2", time.Time{}, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("expected 1 record for u2, got %+v err=%v", got, err)
	}
}

func TestPruneKeepsMinimum(t *testing.T) {
	db := testDB(t)
	old := time.Now().Add(-48 * time.Hour)
	for i := 0; i < 5; i++ {
		rec := storage.ActivityLog{ID: string(rune('a' + i)), Event: "test", CreatedAt: old}
		if err := db.Create(&rec).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	deleted, err := Prune(db, time.Now().Add(-24*time.Hour), 2)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 3 {
		t.Fatalf("expected 3 deleted, got %d", deleted)
	}
	var n int64
	db.Model(&storage.ActivityLog{}).Count(&n)
	if n != 2 {
		t.Fatalf("expected 2 kept, got %d", n)
	}
}

func TestLog_ServerRestart(t *testing.T) {
	db := testDB(t)
	if err := Log(db, Entry{
		ActorID:    "u1",
		ActorName:  "admin",
		IP:         "127.0.0.1",
		Event:      EventServerRestart,
		SubjectTyp: "server",
		SubjectID:  "s1",
	}); err != nil {
		t.Fatalf("Log failed: %v", err)
	}

	got, err := List(db, "s1", "", time.Time{}, 10)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(got) != 1 || got[0].Event != EventServerRestart {
		t.Fatalf("expected EventServerRestart, got %+v", got)
	}
}
