// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodestorage

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateZipWritesOnlyThroughAccessor(t *testing.T) {
	acc, root := testAccessor(t)
	if err := acc.WriteFile("world/level.dat", []byte("level")); err != nil {
		t.Fatal(err)
	}
	if err := acc.WriteFile("server.properties", []byte("motd=hi")); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "controller-sibling")
	n, err := CreateZip(context.Background(), acc, []string{"."}, "backups/world.zip", true)
	if err != nil {
		t.Fatalf("CreateZip: %v", err)
	}
	if n != 2 {
		t.Fatalf("archived %d, want 2", n)
	}

	raw, err := acc.ReadFile("backups/world.zip")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		got[f.Name] = string(body)
	}
	if got["world/level.dat"] != "level" || got["server.properties"] != "motd=hi" {
		t.Fatalf("zip contents = %v", got)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("create zip must not create %s: %v", outside, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "backups")); !os.IsNotExist(err) {
		t.Fatalf("create zip must not write beside the accessor root: %v", err)
	}
}

func TestCreateZipRejectsTraversal(t *testing.T) {
	acc, root := testAccessor(t)
	if err := acc.WriteFile("ok.txt", []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateZip(context.Background(), acc, []string{"../evil"}, "out.zip", true); err == nil || !IsEscape(err) {
		t.Fatalf("want escape on source, got %v", err)
	}
	if _, err := CreateZip(context.Background(), acc, []string{"ok.txt"}, "../out.zip", true); err == nil || !IsEscape(err) {
		t.Fatalf("want escape on dest, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "out.zip")); !os.IsNotExist(err) {
		t.Fatalf("traversal wrote outside the accessor root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "evil")); !os.IsNotExist(err) {
		t.Fatalf("traversal source must not create %s: %v", filepath.Join(filepath.Dir(root), "evil"), err)
	}
}

func TestWriteZipStreamsFromAccessor(t *testing.T) {
	acc, _ := testAccessor(t)
	if err := acc.WriteFile("a.txt", []byte("aaa")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	n, err := WriteZip(context.Background(), acc, []string{"a.txt"}, &buf, true)
	if err != nil {
		t.Fatalf("WriteZip: %v", err)
	}
	if n != 1 {
		t.Fatalf("archived %d, want 1", n)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "a.txt" {
		t.Fatalf("entries = %v", zr.File)
	}
}
